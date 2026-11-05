package api

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/soroworks/sorovault/internal/store"
)

type tokenNameKey struct{}

func withTokenName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, tokenNameKey{}, name)
}

func tokenNameFromContext(ctx context.Context) string {
	name, _ := ctx.Value(tokenNameKey{}).(string)
	return name
}

// parseTokenName splits an optional "name:secret" prefix from a configured
// token. When no name prefix is present, fallback is used as the token's audit
// label so the raw secret is never stored or returned.
func parseTokenName(raw, fallback string) (name, secret string) {
	if prefix, rest, ok := strings.Cut(raw, ":"); ok && prefix != "" && rest != "" {
		return prefix, rest
	}
	return fallback, raw
}

func (s *Server) clientAddress(r *http.Request) string {
	if s.rateLimit != nil {
		return s.rateLimit.clientKey(r)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) recordAudit(r *http.Request, action, network, contractID string) {
	err := s.reg.Store().RecordAudit(r.Context(), store.AuditEntry{
		Action:     action,
		Network:    network,
		ContractID: contractID,
		ClientIP:   s.clientAddress(r),
		TokenName:  tokenNameFromContext(r.Context()),
	})
	if err != nil {
		s.log.WarnContext(r.Context(), "recording audit entry failed", "action", action, "error", err)
	}
}

// readProtected gates an endpoint behind bearer authentication when API tokens
// are configured, while still allowing reads in read-only mode.
func (s *Server) readProtected(h http.HandlerFunc) http.HandlerFunc {
	if len(s.tokens) > 0 {
		return s.requireBearer(h)
	}
	return h
}

// listAudit serves GET /api/audit.
func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.AuditFilter{
		Network:    q.Get("network"),
		ContractID: q.Get("contract_id"),
		Action:     q.Get("action"),
		Limit:      intParam(q.Get("limit"), 0),
		Offset:     intParam(q.Get("offset"), 0),
	}
	filter.Normalize()

	page, err := s.reg.Store().ListAudit(r.Context(), filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, auditListResponse{
		Entries: page.Entries,
		Total:   page.Total,
		Limit:   filter.Limit,
		Offset:  filter.Offset,
	})
}
