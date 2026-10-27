package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/soroworks/sorovault/internal/attest"
	"github.com/soroworks/sorovault/internal/codegen"
	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/store"
)

// parseWasmHash normalises a WASM hash from the URL.
//
// A hash is the SHA-256 of the module, so it is 32 bytes of hexadecimal;
// the hex is lower-cased because that is how the registry stores it.
func parseWasmHash(raw string) (string, error) {
	rawHash, err := hex.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(rawHash) != sha256.Size {
		return "", fmt.Errorf("%w: %q", registry.ErrInvalidWasmHash, raw)
	}
	return hex.EncodeToString(rawHash), nil
}

func (s *Server) getWasmSpec(w http.ResponseWriter, r *http.Request) {
	wasmHash, err := parseWasmHash(chi.URLParam(r, "hash"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid wasm hash")
		return
	}
	network, err := s.network(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	stored, err := s.reg.Store().GetWasmSpec(r.Context(), network, wasmHash)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if s.signer != nil {
		body, err := json.Marshal(stored.Interface)
		if err != nil {
			s.fail(w, r, fmt.Errorf("api: encoding interface for wasm %s: %w", wasmHash, err))
			return
		}
		s.attest(w, r, attest.Statement{
			Network: stored.Network, WasmHash: stored.WasmHash, Interface: body,
		})
	}
	writeJSON(w, http.StatusOK, stored)
}

// listWasmContracts serves GET /api/wasm/{hash}/contracts.
//
// It answers "who else runs this code?", which is the question a
// vulnerability in a shared WASM raises: every instance of a token
// template, every vault a factory deployed. ?ever=true widens the answer
// to the contracts that ran the code in the past and have since been
// upgraded.
func (s *Server) listWasmContracts(w http.ResponseWriter, r *http.Request) {
	wasmHash, err := parseWasmHash(chi.URLParam(r, "hash"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid wasm hash")
		return
	}

	network, err := s.network(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	q := r.URL.Query()
	filter := store.WasmFilter{
		Network:  network,
		WasmHash: wasmHash,
		Ever:     boolParam(q.Get("ever")),
		Limit:    intParam(q.Get("limit"), 0),
		Offset:   intParam(q.Get("offset"), 0),
	}
	filter.Normalize()

	page, err := s.reg.Store().ListContractsByWasm(r.Context(), filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, wasmContractsResponse{
		WasmHash:  wasmHash,
		Network:   network,
		Ever:      filter.Ever,
		Contracts: page.Contracts,
		Total:     page.Total,
		Limit:     filter.Limit,
		Offset:    filter.Offset,
	})
}

func (s *Server) catalogWasm(w http.ResponseWriter, r *http.Request) {
	result, err := s.reg.CatalogWasm(r.Context(), chi.URLParam(r, "hash"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, result)
}

// maxDecodeBody caps an uploaded module at 512 KiB — four times the 128 KiB
// the network allows as contract code, so any module that could ever run on
// chain fits. It stays deliberately small because this is the only endpoint
// that reads an arbitrary blob, and each concurrent upload is held in full:
// until per-client rate limiting exists, the body limit is what bounds
// memory under a flood of requests.
const maxDecodeBody = 512 << 10

// errDecodeTooLarge reports an upload beyond maxDecodeBody, so the handler
// can answer 413 instead of the 400 every other malformed body earns.
var errDecodeTooLarge = fmt.Errorf("request body exceeds the %d-byte limit", maxDecodeBody)

// decodeWasm serves POST /api/decode: it decodes an uploaded WASM module
// and returns its interface, without storing anything. The endpoint touches
// neither the network nor the database, and reuses spec.XDRDecoder — the
// same decoder every other path goes through, so its limits apply here
// unchanged.
//
// The body is either the raw module, whatever the content type, or a
// multipart/form-data upload whose first file part carries it. ?format=ts
// answers with the TypeScript client for the interface instead of the ABI
// JSON.
func (s *Server) decodeWasm(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format != "" && format != "json" && format != "ts" {
		writeError(w, http.StatusBadRequest, "format must be \"json\" or \"ts\"")
		return
	}

	module, err := readWasmBody(r, maxDecodeBody)
	switch {
	case errors.Is(err, errDecodeTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Every decoder failure is a property of the upload — an unparseable
	// module, a missing spec section, an entry stream that does not
	// convert — never a server fault, so none of it becomes a 500.
	var decoder spec.XDRDecoder
	iface, err := decoder.Decode(module)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	if format == "ts" {
		// There is no contract ID or network yet — the module is not
		// deployed — so only the hash, derived from the bytes at hand,
		// identifies the interface version the client was built from.
		sum := sha256.Sum256(module)
		source, err := codegen.TypeScript(iface, codegen.Source{
			WasmHash: hex.EncodeToString(sum[:]),
		})
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(source))
		return
	}

	writeJSON(w, http.StatusOK, iface)
}

// readWasmBody reads the uploaded module from r: a raw body, or the first
// file part of a multipart/form-data upload. It rejects an empty body and
// anything larger than limit, which is enforced as the bytes arrive, so an
// oversized upload is never held in full.
func readWasmBody(r *http.Request, limit int) ([]byte, error) {
	var body io.Reader = r.Body

	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && mediaType == "multipart/form-data" {
		file, err := multipartWasmFile(r)
		if err != nil {
			return nil, err
		}
		body = file
	}

	module, err := io.ReadAll(io.LimitReader(body, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("reading request body: %w", err)
	}
	if len(module) > limit {
		return nil, errDecodeTooLarge
	}
	if len(module) == 0 {
		return nil, errors.New("request body is empty")
	}
	return module, nil
}

// multipartWasmFile returns the first file part of a multipart upload — the
// part a browser or curl calls a file. Plain fields are skipped, so a form
// carrying no file is refused rather than decoded as if its field names
// were a WASM module.
func multipartWasmFile(r *http.Request) (io.Reader, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, fmt.Errorf("invalid multipart body: %w", err)
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("multipart body contains no file part")
		}
		if err != nil {
			return nil, fmt.Errorf("invalid multipart body: %w", err)
		}
		if part.FileName() != "" {
			return part, nil
		}
	}
}
