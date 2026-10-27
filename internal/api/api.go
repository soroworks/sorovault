// Package api serves the registry over HTTP as JSON.
//
// The API is read-mostly: the only write is registering or refreshing a
// contract, which does not change the contract itself, only what SoroVault
// knows about it. There is no authentication by default — deployment is
// expected to put the service behind whatever the operator already uses —
// and when API tokens are configured, write endpoints require a bearer
// credential while reads stay public.
package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	_ "embed"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/soroworks/sorovault/internal/attest"
	"github.com/soroworks/sorovault/internal/refresher"
	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/stellar"
	"github.com/soroworks/sorovault/internal/store"
	"github.com/soroworks/sorovault/internal/wasm"
)

// Server holds the API's dependencies.
type Server struct {
	reg       *registry.Registry
	log       *slog.Logger
	cache     *interfaceCache
	rateLimit *clientLimiter

	// lastSweep, when set, reports the most recent automatic refresh
	// sweep. It is nil when refresh is disabled, and GET /api/stats then
	// omits the field.
	lastSweep func() (refresher.SweepRecord, bool)

	// signer, when set, signs interface responses. keys is every public key
	// published at GET /api/keys: the signer's, then any retired ones.
	signer *attest.Signer
	keys   []attest.Key

	// readOnly marks a mirror that refuses writes. It is read once, when
	// Routes mounts, so a server's mode is fixed at startup.
	readOnly bool

	// tokens are the bearer credentials writes must present. Empty keeps
	// every write public, as the API has always behaved; they are held
	// here and compared in constant time, and never logged or echoed.
	tokens []string
}

// NewServer builds an API server over a registry.
func NewServer(reg *registry.Registry, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{reg: reg, log: log, cache: newInterfaceCache(interfaceCacheBytes)}
}

// SetReadOnly makes the server's write routes refuse with 405 instead of
// running: reads stay public, and the automatic refresher outside the API
// keeps the registry current. Call it before Routes mounts the routes,
// where the choice between a real handler and a refusal is made once. It
// mirrors SetLastSweep, which serve also sets at startup.
func (s *Server) SetReadOnly(readOnly bool) {
	s.readOnly = readOnly
}

// SetAPITokens requires write routes to present one of the given bearer
// tokens. Call it before Routes mounts, where the gate is built once.
// An empty list keeps every write public; values are compared in constant
// time, and nothing logs or echoes them, matching how the config keeps
// them out of trouble.
func (s *Server) SetAPITokens(tokens []string) {
	s.tokens = tokens
}

// SetLastSweep makes GET /api/stats report the most recent automatic
// refresh sweep. Serve calls it when refresh is enabled; without it the
// field is omitted, which is the honest answer when nothing sweeps.
func (s *Server) SetLastSweep(fn func() (refresher.SweepRecord, bool)) {
	s.lastSweep = fn
}

// SetSigner makes the server attach a detached Ed25519 signature to every
// interface response, and publish the verification keys at GET /api/keys.
// retired are earlier public keys that stay published so signatures made
// before a rotation remain verifiable.
func (s *Server) SetSigner(signer *attest.Signer, retired ...ed25519.PublicKey) {
	s.signer = signer
	s.keys = nil
	if signer == nil {
		return
	}
	s.keys = append(s.keys, attest.NewKey(signer.PublicKey(), true))
	for _, pub := range retired {
		if key := attest.NewKey(pub, false); key.ID != s.keys[0].ID {
			s.keys = append(s.keys, key)
		}
	}
}

// Routes returns the API's routes, to be mounted under /api.
func (s *Server) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(compressResponses("application/json", "text/plain"))

	r.Get("/contracts", s.listContracts)
	// POST /decode stays available in read-only mode: it answers with the
	// decoded module and stores nothing, so it is a read with POST form.
	r.Post("/decode", s.rateLimited(s.decodeWasm))
	r.Post("/contracts", s.write(s.rateLimited(s.registerContract)))
	r.Post("/contracts:batch", s.write(s.batchRegisterContracts))
	r.Get("/wasm/{hash}", s.getWasmSpec)
	r.Post("/wasm/{hash}", s.write(s.catalogWasm))
	r.Get("/wasm/{hash}/contracts", s.listWasmContracts)
	r.Get("/contracts/{id}", s.getContract)
	r.Put("/contracts/{id}/source", s.write(s.updateContractSource))
	r.Post("/contracts/{id}/verify", s.write(s.verifyPublisher))
	r.Put("/contracts/{id}/deprecation", s.write(s.setContractDeprecation))
	r.Delete("/contracts/{id}/deprecation", s.write(s.clearContractDeprecation))
	r.Post("/contracts/{id}/refresh", s.write(s.rateLimited(s.refreshContract)))
	r.Get("/contracts/{id}/versions", s.listVersions)
	r.Get("/contracts/{id}/functions", s.listFunctions)
	r.Get("/contracts/{id}/events", s.listEvents)
	r.Get("/contracts/{id}/functions/{fn}", s.getFunction)
	r.Get("/contracts/{id}/functions/{fn}/schema.json", s.getFunctionSchema)
	r.Get("/contracts/{id}/functions/{fn}/example.json", s.getFunctionExample)
	r.Get("/contracts/{id}/client.ts", s.getTypeScriptClient)
	r.Get("/keys", s.getKeys)
	r.Get("/stats", s.getStats)
	r.Get("/upgrades", s.listUpgrades)
	r.Get("/upgrades.atom", s.upgradesAtom)
	r.Get("/openapi.json", serveOpenAPI)
	r.Post("/subscriptions", s.write(s.subscribeContract))
	r.Get("/subscriptions", s.listSubscriptions)
	r.Delete("/subscriptions/{secret}", s.write(s.unsubscribeContract))

	return r
}

// write chooses at mount time what a write route does: its real handler in
// normal operation, or a 405 refusal in read-only mode. The routes stay
// mounted either way, so a writer hitting a read-only mirror is told the
// method is refused rather than sent a 404 that reads as an unknown
// resource.
func (s *Server) write(h http.HandlerFunc) http.HandlerFunc {
	if s.readOnly {
		return func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusMethodNotAllowed,
				"this SoroVault is running read-only; write access is disabled")
		}
	}
	if len(s.tokens) > 0 {
		return s.requireBearer(h)
	}
	return h
}

// requireBearer serves the wrapped route only when the request presents
// one of the configured tokens as `Authorization: Bearer <token>`.
//
// Malformed credentials and wrong tokens get different status codes — a
// malformed header is a client mistake worth teaching, while a valid
// header with the wrong secret is an access attempt and only ever earns
// "unauthorized", which carries the presented secret nowhere.
func (s *Server) requireBearer(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			writeError(w, http.StatusBadRequest,
				"Authorization header must be Bearer authentication")
			return
		}
		if !s.tokenMatches(token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="sorovault"`)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

// tokenMatches compares a presented secret against every configured token
// regardless of any match: the loop runs whole, so success is not told
// apart from failure by how early it stopped. Every comparison is
// constant time; lengths differ, subtleties and all, so timing leaks
// nothing more than the response does.
func (s *Server) tokenMatches(presented string) bool {
	matches := 0
	for _, token := range s.tokens {
		matches |= subtle.ConstantTimeCompare([]byte(presented), []byte(token))
	}
	return matches == 1
}

// attest attaches a detached signature over the statement to the response.
//
// A signing failure leaves the response unsigned and logs it: the data is
// still right, and a client that requires a signature will notice it is
// missing and refuse the response, which is what the signature is for.
func (s *Server) attest(w http.ResponseWriter, r *http.Request, st attest.Statement) {
	if s.signer == nil {
		return
	}
	sig, err := s.signer.Sign(st)
	if err != nil {
		s.log.ErrorContext(r.Context(), "signing interface response failed")
		return
	}
	w.Header().Set(attest.SignatureHeader, sig)
	w.Header().Set(attest.KeyIDHeader, s.signer.KeyID())
}

// getKeys serves GET /api/keys: the public keys interface signatures verify
// against. The list is empty when the registry does not sign.
func (s *Server) getKeys(w http.ResponseWriter, _ *http.Request) {
	keys := s.keys
	if keys == nil {
		keys = []attest.Key{}
	}
	writeJSON(w, http.StatusOK, keysResponse{Keys: keys})
}

// openAPISpec describes every route above. TestOpenAPICoversEveryRoute
// fails if a route is added without documenting it, or documented without
// existing.
//
//go:embed openapi.json
var openAPISpec []byte

func serveOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(openAPISpec)
}

// minCompressBytes is the size below which the extra encoding work and
// headers cost more than they save. Health checks, small errors and tiny
// listings stay plain.
const minCompressBytes = 1024

// compressResponses gzips large responses of the listed content types when
// the client accepts gzip.
//
// It buffers the response before deciding, because chi's compressor decides
// at WriteHeader time while the size is only known after the body is written.
// API responses are bounded and already materialised in memory, so the delay
// is not visible; the win is that a small response never carries a
// Content-Encoding it did not need.
func compressResponses(types ...string) func(http.Handler) http.Handler {
	compressor := middleware.NewCompressor(5, types...)
	compressible := make(map[string]struct{}, len(types))
	for _, contentType := range types {
		compressible[contentType] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
				next.ServeHTTP(w, r)
				return
			}

			buffer := newResponseBuffer()
			next.ServeHTTP(buffer, r)

			contentType, _, _ := strings.Cut(buffer.Header().Get("Content-Type"), ";")
			_, allowed := compressible[contentType]
			if buffer.body.Len() < minCompressBytes || !allowed ||
				buffer.Header().Get("Content-Encoding") != "" {
				buffer.writeTo(w)
				return
			}

			compressor.Handler(http.HandlerFunc(func(cw http.ResponseWriter, _ *http.Request) {
				copyHeader(cw.Header(), buffer.Header())
				cw.WriteHeader(buffer.status)
				_, _ = cw.Write(buffer.body.Bytes())
			})).ServeHTTP(w, r)
		})
	}
}

// acceptsGzip reports whether the header allows gzip, honouring an explicit
// q=0 refusal.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		if !strings.EqualFold(strings.TrimSpace(fields[0]), "gzip") {
			continue
		}
		for _, parameter := range fields[1:] {
			name, value, ok := strings.Cut(parameter, "=")
			if ok && strings.EqualFold(strings.TrimSpace(name), "q") &&
				strings.TrimSpace(value) == "0" {
				return false
			}
		}
		return true
	}
	return false
}

// responseBuffer records a response so compression can be decided once its
// full size is known.
type responseBuffer struct {
	header      http.Header
	status      int
	body        bytes.Buffer
	wroteHeader bool
}

func newResponseBuffer() *responseBuffer {
	return &responseBuffer{header: make(http.Header), status: http.StatusOK}
}

func (b *responseBuffer) Header() http.Header { return b.header }

func (b *responseBuffer) WriteHeader(status int) {
	if b.wroteHeader {
		return
	}
	b.wroteHeader = true
	b.status = status
}

func (b *responseBuffer) Write(p []byte) (int, error) {
	if !b.wroteHeader {
		b.WriteHeader(http.StatusOK)
	}
	return b.body.Write(p)
}

func (b *responseBuffer) writeTo(w http.ResponseWriter) {
	copyHeader(w.Header(), b.header)
	w.WriteHeader(b.status)
	_, _ = w.Write(b.body.Bytes())
}

func copyHeader(dst, src http.Header) {
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
}

// Middleware returns the stack the whole server runs behind: a request ID, a
// panic guard, and a bound on how long any one request may take.
//
// There is deliberately no RealIP middleware: it trusts X-Forwarded-For from
// any client, and nothing here needs the client address.
func Middleware(log *slog.Logger) []func(http.Handler) http.Handler {
	return []func(http.Handler) http.Handler{
		middleware.RequestID,
		requestIDHeader,
		middleware.Recoverer,
		requestLogger(log),
	}
}

// requestIDHeader copies the request ID onto the response, so a client can
// quote it when reporting a problem. The ID itself is assigned by
// middleware.RequestID.
func requestIDHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := middleware.GetReqID(r.Context()); id != "" {
			w.Header().Set(middleware.RequestIDHeader, id)
		}
		next.ServeHTTP(w, r)
	})
}

// network resolves which network a read applies to: the explicit query
// parameter if given, otherwise the one this instance is pointed at.
func (s *Server) network(r *http.Request) (string, error) {
	if n := strings.TrimSpace(r.URL.Query().Get("network")); n != "" {
		return n, nil
	}
	return s.reg.Network(r.Context())
}

// fail maps a domain error onto a status code and writes it.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if ctxErr := r.Context().Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		// The client went away; there is nobody left to read a response.
		return
	}

	status, message := s.errorStatus(r, err)
	if errors.Is(err, registry.ErrBusy) {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(s.reg.AcquireTimeout())))
	}
	writeError(w, status, message)
}

// errorStatus maps a domain error onto the status code it deserves and the
// message that may be shown to the caller.
//
// Everything unrecognised becomes a 500 with a generic body, so an internal
// failure cannot leak a connection string or query text to a caller.
func (s *Server) errorStatus(r *http.Request, err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrMalformedCursor):
		// A cursor is a client-typed string; an unreadable one is the
		// caller's mistake, not the server's.
		return http.StatusBadRequest, err.Error()

	case errors.Is(err, store.ErrNotFound), errors.Is(err, stellar.ErrContractNotFound), errors.Is(err, stellar.ErrWasmNotFound):
		return http.StatusNotFound, err.Error()

	case errors.Is(err, stellar.ErrNoWasm), errors.Is(err, spec.ErrNoSpecSection):
		// The request was well-formed and the contract exists; it just has
		// no interface to serve.
		return http.StatusUnprocessableEntity, err.Error()

	case errors.Is(err, spec.ErrLimitExceeded), errors.Is(err, wasm.ErrModuleTooLarge):
		// The module or its spec section is larger or deeper than SoroVault
		// will decode. That is a property of the contract, not a server
		// fault, so it is a 422 with the limit in the message rather than a
		// 500.
		return http.StatusUnprocessableEntity, err.Error()

	case errors.Is(err, registry.ErrNetworkMismatch):
		return http.StatusServiceUnavailable, err.Error()

	case errors.Is(err, registry.ErrBusy):
		return http.StatusServiceUnavailable, err.Error()

	case errors.Is(err, registry.ErrInvalidClaim):
		return http.StatusBadRequest, err.Error()

	case errors.Is(err, registry.ErrNotDeployer):
		return http.StatusForbidden, err.Error()

	case errors.Is(err, registry.ErrStaleClaim), errors.Is(err, registry.ErrDeployerUnknown):
		return http.StatusUnprocessableEntity, err.Error()

	case isBadContractID(err), errors.Is(err, registry.ErrInvalidWasmHash):
		return http.StatusBadRequest, err.Error()

	default:
		s.log.ErrorContext(r.Context(), "request failed")
		return http.StatusInternalServerError, "internal error"
	}
}

// isBadContractID reports whether err came from strkey rejecting an address.
func isBadContractID(err error) bool {
	return strings.Contains(err.Error(), "is not a valid contract address") ||
		strings.Contains(err.Error(), "is not a contract address")
}

// intParam parses a query parameter, falling back on anything unparseable
// rather than rejecting the request over a malformed paging hint.
func intParam(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

// boolParam parses a query parameter as a flag, treating anything
// unparseable as false: the parameter only ever widens an answer, so a
// malformed value must not widen it by accident.
func boolParam(raw string) bool {
	v, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return v
}

// retryAfterSeconds renders a wait as the whole seconds a client should
// wait before retrying, which is what the Retry-After header asks for.
func retryAfterSeconds(d time.Duration) int {
	seconds := int((d + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}
