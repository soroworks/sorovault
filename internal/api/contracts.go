package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/soroworks/sorovault/internal/attest"
	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/stellar"
	"github.com/soroworks/sorovault/internal/store"
)

var commitHash = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

func (s *Server) updateContractSource(w http.ResponseWriter, r *http.Request) {
	var req sourceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sourceURL := strings.TrimSpace(req.SourceURL)
	parsed, err := url.Parse(sourceURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		writeError(w, http.StatusBadRequest, "source_url must be an absolute HTTP or HTTPS repository URL")
		return
	}
	commit := strings.TrimSpace(req.SourceCommit)
	if commit != "" && !commitHash.MatchString(commit) {
		writeError(w, http.StatusBadRequest, "source_commit must be a 40- or 64-character hexadecimal commit hash")
		return
	}
	network, err := s.network(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	contractID := chi.URLParam(r, "id")
	if err := s.reg.Store().UpdateSource(r.Context(), network, contractID, sourceURL, commit); err != nil {
		s.fail(w, r, err)
		return
	}
	contract, err := s.reg.Store().GetContract(r.Context(), network, contractID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, contract)
}

// verifyPublisher serves POST /api/contracts/{id}/verify.
//
// The body is a claim signed by the account that deployed the contract; see
// registry.VerifyPublisher for what makes one acceptable. Nothing else
// authenticates the caller, because the signature is the authentication.
func (s *Server) verifyPublisher(w http.ResponseWriter, r *http.Request) {
	var req verifyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Claim.ContractID != chi.URLParam(r, "id") {
		writeError(w, http.StatusBadRequest, "claim.contract_id does not match the contract in the URL")
		return
	}

	contract, err := s.reg.VerifyPublisher(r.Context(), registry.PublisherClaim{
		Claim:     req.Claim,
		Signer:    strings.TrimSpace(req.Signer),
		Signature: strings.TrimSpace(req.Signature),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, contract)
}

// deprecationRequest is the body of PUT /contracts/{id}/deprecation. Both
// fields are optional: a contract can be retired without a recorded
// destination.
type deprecationRequest struct {
	Successor string `json:"successor,omitempty"`
	Note      string `json:"note,omitempty"`
}

// setContractDeprecation serves PUT
// /api/contracts/{id}/deprecation: mark a contract deprecated, with an
// optional successor and note. It answers with the updated contract, the
// same shape the source endpoint answers with.
func (s *Server) setContractDeprecation(w http.ResponseWriter, r *http.Request) {
	var req deprecationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	network, err := s.network(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	contractID := chi.URLParam(r, "id")
	successor := strings.TrimSpace(req.Successor)
	if successor == contractID {
		writeError(w, http.StatusBadRequest,
			"the successor must be a contract other than the one being deprecated")
		return
	}
	if successor != "" {
		if _, err := s.reg.Store().GetContract(r.Context(), network, successor); err != nil {
			writeError(w, http.StatusBadRequest, "successor "+successor+" is not registered on "+network)
			return
		}
	}
	if err := s.reg.Store().SetDeprecation(r.Context(), network, contractID, successor,
		strings.TrimSpace(req.Note)); err != nil {
		s.fail(w, r, err)
		return
	}
	contract, err := s.reg.Store().GetContract(r.Context(), network, contractID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, contract)
}

// clearContractDeprecation serves DELETE
// /api/contracts/{id}/deprecation: lift the mark, restoring the contract
// to active use.
func (s *Server) clearContractDeprecation(w http.ResponseWriter, r *http.Request) {
	network, err := s.network(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	contractID := chi.URLParam(r, "id")
	if err := s.reg.Store().ClearDeprecation(r.Context(), network, contractID); err != nil {
		s.fail(w, r, err)
		return
	}
	contract, err := s.reg.Store().GetContract(r.Context(), network, contractID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, contract)
}

// listContracts serves GET /api/contracts.
//
// Paging is either by cursor or by the deprecated offset. A cursor names the
// last row of the previous page, so rows added while a client pages through
// the listing are seen exactly once — which is the property offsets lose.
func (s *Server) listContracts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	cursor := q.Get("cursor")
	offset := intParam(q.Get("offset"), 0)
	if cursor != "" && offset != 0 {
		writeError(w, http.StatusBadRequest,
			"give a cursor or an offset, not both; offset is deprecated, so prefer the cursor")
		return
	}

	filter := store.ListFilter{
		Network: q.Get("network"),
		Query:   q.Get("q"),
		// The exact-name filters are repeatable, and every one of them has
		// to match.
		Functions:  q["function"],
		Types:      q["type"],
		Implements: q["implements"],
		Limit:      intParam(q.Get("limit"), 0),
		Offset:     offset,
		Cursor:     cursor,
	}
	filter.Normalize()

	page, err := s.reg.Store().ListContracts(r.Context(), filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, listResponse{
		Contracts: page.Contracts,
		Total:     page.Total,
		Limit:     filter.Limit,
		Offset:    filter.Offset,
		Next:      page.Next,
	})
}

// getContract serves GET /api/contracts/{id}, returning the decoded ABI.
//
// The optional wasm_hash parameter selects a superseded version; without it
// the contract's current interface is returned. ?download=1 asks the browser
// to save the document under a name of its own rather than display it.
func (s *Server) getContract(w http.ResponseWriter, r *http.Request) {
	contractID := chi.URLParam(r, "id")

	network, err := s.network(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	contract, err := s.reg.Store().GetContract(r.Context(), network, contractID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	wasmHash := strings.TrimSpace(r.URL.Query().Get("wasm_hash"))
	cacheHash := wasmHash
	if cacheHash == "" {
		cacheHash = contract.CurrentWasmHash
	}
	key := specCacheKey{network: network, contractID: contractID, wasmHash: cacheHash}
	cached, ok := s.cache.get(key)
	if !ok {
		stored, err := s.reg.Store().GetSpec(r.Context(), network, contractID, cacheHash)
		if err != nil {
			s.fail(w, r, err)
			return
		}

		body, err := json.Marshal(stored.Interface)
		if err != nil {
			s.fail(w, r, fmt.Errorf("api: encoding interface for %s: %w", contractID, err))
			return
		}
		cached = cachedInterface{body: body, decodedAt: stored.DecodedAt.UTC()}
		s.cache.add(key, cached)
	}

	// The header has to be set before the body is written.
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+ABIFilename(contractID, cacheHash)+`"`)
	}

	builtin := cacheHash == stellar.BuiltinSACIdentifier
	responseHash := cacheHash
	interfaceID := ""
	if builtin {
		responseHash = ""
		interfaceID = stellar.BuiltinSACIdentifier
	}
	s.attest(w, r, attest.Statement{
		Network: contract.Network, ContractID: contract.ContractID,
		WasmHash: cacheHash, Interface: cached.body,
	})
	writeJSON(w, http.StatusOK, contractResponse{
		ContractID:   contract.ContractID,
		Network:      contract.Network,
		WasmHash:     responseHash,
		InterfaceID:  interfaceID,
		Builtin:      builtin,
		Name:         contract.Name,
		Implements:   contract.Implements,
		SourceURL:    contract.SourceURL,
		SourceCommit: contract.SourceCommit,
		FirstSeen:    contract.FirstSeen.UTC(),
		DecodedAt:    cached.decodedAt,
		MissingSince: contract.MissingSince,
		Origin:       originOf(*contract),
		Verified:     verifiedOf(*contract),
		Current:      cacheHash == contract.CurrentWasmHash,
		Interface:    json.RawMessage(cached.body),
	})
}

// ABIFilename names the file a downloaded interface is saved as: the contract
// it belongs to, and the short form of the WASM hash it was decoded from, so
// two versions of one contract do not collide.
//
// The browse page names the file the same way, so the two cannot disagree
// about what a download will be called.
func ABIFilename(contractID, wasmHash string) string {
	return contractID + "." + shortHash(wasmHash) + ".abi.json"
}

// shortHash is the head of a WASM hash: enough to tell two versions of a
// contract apart, short enough for a filename.
func shortHash(hash string) string {
	const n = 12
	if len(hash) <= n {
		return hash
	}
	return hash[:n]
}

// getFunction serves GET /api/contracts/{id}/functions/{fn}.
func (s *Server) getFunction(w http.ResponseWriter, r *http.Request) {
	contractID := chi.URLParam(r, "id")
	fnName := chi.URLParam(r, "fn")

	network, err := s.network(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	wasmHash := strings.TrimSpace(r.URL.Query().Get("wasm_hash"))
	stored, err := s.reg.Store().GetSpec(r.Context(), network, contractID, wasmHash)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	fn, ok := stored.Interface.Function(fnName)
	if !ok {
		writeError(w, http.StatusNotFound,
			"no function "+strconv.Quote(fnName)+" in contract "+contractID)
		return
	}

	wasmHash, interfaceID := stored.WasmHash, ""
	if stored.Interface.Builtin {
		wasmHash = ""
		interfaceID = stored.WasmHash
	}
	writeJSON(w, http.StatusOK, functionResponse{
		ContractID:  stored.ContractID,
		Network:     stored.Network,
		WasmHash:    wasmHash,
		InterfaceID: interfaceID,
		Builtin:     stored.Interface.Builtin,
		Signature:   fn.Signature(),
		Function:    fn,
	})
}

// listFunctions serves GET /api/contracts/{id}/functions.
func (s *Server) listFunctions(w http.ResponseWriter, r *http.Request) {
	contractID := chi.URLParam(r, "id")

	network, err := s.network(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	wasmHash := strings.TrimSpace(r.URL.Query().Get("wasm_hash"))
	stored, err := s.reg.Store().GetSpec(r.Context(), network, contractID, wasmHash)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	functions := make([]functionSummary, 0, len(stored.Interface.Functions))
	for _, fn := range stored.Interface.Functions {
		functions = append(functions, functionSummary{
			Name:      fn.Name,
			Signature: fn.Signature(),
		})
	}

	wasmHash, interfaceID := stored.WasmHash, ""
	if stored.Interface.Builtin {
		wasmHash = ""
		interfaceID = stored.WasmHash
	}
	writeJSON(w, http.StatusOK, functionListResponse{
		ContractID:  stored.ContractID,
		Network:     stored.Network,
		WasmHash:    wasmHash,
		InterfaceID: interfaceID,
		Builtin:     stored.Interface.Builtin,
		Functions:   functions,
	})
}

// listEvents serves GET /api/contracts/{id}/events: the event definitions a
// contract declares, for the indexers that react to them.
//
// An indexer usually wants only the events and not the whole interface, which
// is one large document. Each event is the definition the interface holds
// itself — its name, topic names, parameters with their types and whether
// each is a topic or data, and the data payload's packing — so values here
// match the ones the interface document serves, `?wasm_hash=` selects a
// superseded version the same way, and the shapes stay in step by
// construction.
func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	contractID := chi.URLParam(r, "id")

	network, err := s.network(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	wasmHash := strings.TrimSpace(r.URL.Query().Get("wasm_hash"))
	stored, err := s.reg.Store().GetSpec(r.Context(), network, contractID, wasmHash)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	events := stored.Interface.Events
	if events == nil {
		// A contract with no events still answers with an empty list,
		// the way the interface document answers with [].
		events = []model.Event{}
	}

	writeJSON(w, http.StatusOK, eventListResponse{
		ContractID: stored.ContractID,
		Network:    stored.Network,
		WasmHash:   stored.WasmHash,
		Events:     events,
	})
}

// listVersions serves GET /api/contracts/{id}/versions.
func (s *Server) listVersions(w http.ResponseWriter, r *http.Request) {
	contractID := chi.URLParam(r, "id")

	network, err := s.network(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	versions, err := s.reg.Store().ListVersions(r.Context(), network, contractID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, versionsResponse{
		ContractID: contractID,
		Network:    network,
		Versions:   versions,
	})
}

// registerContract serves POST /api/contracts.
func (s *Server) registerContract(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	contractID := strings.TrimSpace(req.ContractID)
	if contractID == "" {
		writeError(w, http.StatusBadRequest, "contract_id is required")
		return
	}

	result, err := s.reg.Register(r.Context(), contractID)
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

// batchRegisterContracts serves POST /api/contracts:batch.
//
// Seeding a registry over the API would otherwise take one request per
// contract. The contracts are registered one after another, so the batch
// draws on the registry's concurrency budget once per contract rather than
// all at once, and one failure does not stop the rest: every contract gets
// a result of its own.
func (s *Server) batchRegisterContracts(w http.ResponseWriter, r *http.Request) {
	var req batchRegisterRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.ContractIDs) == 0 {
		writeError(w, http.StatusBadRequest, "contract_ids must not be empty")
		return
	}
	if len(req.ContractIDs) > MaxBatchContracts {
		writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"at most %d contract IDs per batch, got %d", MaxBatchContracts, len(req.ContractIDs)))
		return
	}

	results := make([]batchResult, 0, len(req.ContractIDs))
	for _, contractID := range req.ContractIDs {
		if r.Context().Err() != nil {
			// The client has gone; there is nobody left to answer.
			return
		}
		results = append(results, s.registerOne(r, contractID))
	}

	response := batchResponse{Results: results}
	for _, result := range results {
		switch result.Status {
		case batchStatusCreated:
			response.Created++
		case batchStatusUnchanged:
			response.Unchanged++
		case batchStatusChanged:
			response.Changed++
		default:
			response.Failed++
		}
	}

	writeJSON(w, http.StatusOK, response)
}

// registerOne registers a single contract and turns the outcome into one
// result of a batch.
func (s *Server) registerOne(r *http.Request, contractID string) batchResult {
	contractID = strings.TrimSpace(contractID)
	if contractID == "" {
		return batchResult{
			Status: batchStatusError,
			Code:   http.StatusBadRequest,
			Error:  "contract_id is required",
		}
	}

	result, err := s.reg.Register(r.Context(), contractID)
	if err != nil {
		status, message := s.errorStatus(r, err)
		return batchResult{
			ContractID: contractID,
			Status:     batchStatusError,
			Code:       status,
			Error:      message,
		}
	}

	status := batchStatusUnchanged
	switch {
	case result.Created:
		status = batchStatusCreated
	case result.Changed:
		status = batchStatusChanged
	}

	return batchResult{
		ContractID: contractID,
		Status:     status,
		Created:    result.Created,
		Changed:    result.Changed,
		Contract:   &result.Contract,
		Interface:  result.Interface,
	}
}

// refreshContract serves POST /api/contracts/{id}/refresh.
func (s *Server) refreshContract(w http.ResponseWriter, r *http.Request) {
	result, err := s.reg.Refresh(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
