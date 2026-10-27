package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/soroworks/sorovault/internal/codegen"
)

// getTypeScriptClient serves GET /api/contracts/{id}/client.ts: a typed
// TypeScript client for the contract's current interface, or for an older
// one with ?wasm_hash=. ?download=1 asks the browser to save it.
func (s *Server) getTypeScriptClient(w http.ResponseWriter, r *http.Request) {
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

	var deprecation *codegen.Deprecation
	if contract, err := s.reg.Store().GetContract(r.Context(), network, contractID); err == nil && contract.DeprecatedSince != nil {
		// The client is generated from the interface, but the mark is
		// on the contract: a generated file exported today should warn
		// its importer about a migration that happened last week.
		deprecation = &codegen.Deprecation{
			Since:     contract.DeprecatedSince.UTC().Format(time.RFC3339),
			Successor: contract.Successor,
			Note:      contract.DeprecationNote,
		}
	}

	source, err := codegen.TypeScript(stored.Interface, codegen.Source{
		ContractID:  stored.ContractID,
		Network:     stored.Network,
		WasmHash:    stored.WasmHash,
		Deprecation: deprecation,
		WithZod:     r.URL.Query().Get("zod") != "",
	})
	if err != nil {
		// The interface decoded, but cannot be expressed as TypeScript —
		// a property of the contract, not a server fault.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+contractID+`.client.ts"`)
	}
	_, _ = w.Write([]byte(source)) //nolint:gosec // generated TypeScript, served as text/plain with nosniff
}

// getFunctionSchema serves GET /api/contracts/{id}/functions/{fn}/schema.json:
// a JSON Schema (2020-12) for the function's arguments.
//
// A wallet or a dApp renders a form from the schema and validates what
// the user typed before encoding it, so the schema has to say what each
// argument may be. It is derived from the same stored interface the rest
// of the API serves, and follows the same type conventions as the typed
// client.
func (s *Server) getFunctionSchema(w http.ResponseWriter, r *http.Request) {
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

	schema, err := codegen.JSONSchema(stored.Interface, fn, codegen.Source{
		ContractID: stored.ContractID,
		Network:    stored.Network,
		WasmHash:   stored.WasmHash,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/schema+json")
	_, _ = w.Write(schema) //nolint:gosec // generated JSON Schema, served as application/schema+json
}

// getFunctionExample serves GET
// /api/contracts/{id}/functions/{fn}/example.json: one example argument list
// for the function.
//
// The document is the placeholder argument object the function's JSON Schema
// validates, so a client can take it, fill in the fields it cares about and
// send its first request. It is derived from the same stored interface the
// schema is, which keeps the two from drifting apart.
func (s *Server) getFunctionExample(w http.ResponseWriter, r *http.Request) {
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

	example, err := codegen.Example(stored.Interface, fn, codegen.Source{
		ContractID: stored.ContractID,
		Network:    stored.Network,
		WasmHash:   stored.WasmHash,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(example) //nolint:gosec // generated example, served as application/json
}
