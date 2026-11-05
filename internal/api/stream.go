package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/soroworks/sorovault/internal/refresher"
	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/store"
)

// SetUpgradeBroker connects the server's GET /api/upgrades/stream endpoint to
// the given in-process upgrade broker.
func (s *Server) SetUpgradeBroker(b *refresher.Broker) {
	if b != nil {
		s.broker = b
	}
}

// UpgradeBroker returns the server's in-process upgrade broker.
func (s *Server) UpgradeBroker() *refresher.Broker {
	return s.broker
}

// publishUpgradeIfChanged notifies the SSE broker when an API registration or
// refresh call records a new interface version for an existing contract.
func (s *Server) publishUpgradeIfChanged(res *registry.Result) {
	if s.broker == nil || res == nil || !res.Changed || res.Created || res.PreviousWasmHash == "" {
		return
	}
	observedAt := res.Contract.LastRefreshed.UTC()
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	s.broker.Publish(store.Upgrade{
		ContractID:  res.Contract.ContractID,
		Network:     res.Contract.Network,
		OldWasmHash: res.PreviousWasmHash,
		NewWasmHash: res.Contract.CurrentWasmHash,
		ObservedAt:  observedAt,
	})
}

// streamUpgrades serves GET /api/upgrades/stream as a Server-Sent Events feed.
//
// When the client supplies a Last-Event-ID header, upgrades recorded after the
// matching event are replayed in chronological order (oldest first) before
// live events are streamed.
func (s *Server) streamUpgrades(w http.ResponseWriter, r *http.Request) {
	network := strings.TrimSpace(r.URL.Query().Get("network"))

	events, unsubscribe, err := s.broker.Subscribe(network)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer unsubscribe()

	lastEventID := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	var replay []store.Upgrade
	if lastEventID != "" {
		replay, err = s.missedUpgrades(r.Context(), network, lastEventID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	sent := make(map[string]struct{}, len(replay))
	for _, u := range replay {
		id := refresher.UpgradeEventID(u)
		if !writeSSEUpgrade(w, flusher, u) {
			return
		}
		sent[id] = struct{}{}
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case u, open := <-events:
			if !open {
				return
			}
			id := refresher.UpgradeEventID(u)
			if _, duplicate := sent[id]; duplicate {
				delete(sent, id)
				continue
			}
			if !writeSSEUpgrade(w, flusher, u) {
				return
			}
		}
	}
}

func writeSSEUpgrade(w http.ResponseWriter, flusher http.Flusher, u store.Upgrade) bool {
	payload, err := json.Marshal(u)
	if err != nil {
		return false
	}
	id := refresher.UpgradeEventID(u)
	if _, err := fmt.Fprintf(w, "id: %s\nevent: upgrade\ndata: %s\n\n", id, payload); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

// missedUpgrades walks the upgrade feed (which is ordered newest first) until
// it finds lastEventID, and returns the upgrades that happened after it in
// chronological order (oldest first). If lastEventID is not found in the feed,
// no historical events are replayed.
func (s *Server) missedUpgrades(ctx context.Context, network, lastEventID string) ([]store.Upgrade, error) {
	var newer []store.Upgrade
	filter := store.UpgradeFilter{Network: network, Limit: store.MaxLimit}
	for {
		page, err := s.reg.Store().ListUpgrades(ctx, filter)
		if err != nil {
			return nil, err
		}
		for _, u := range page.Upgrades {
			if refresher.MatchesUpgradeEventID(u, lastEventID) {
				for i, j := 0, len(newer)-1; i < j; i, j = i+1, j-1 {
					newer[i], newer[j] = newer[j], newer[i]
				}
				return newer, nil
			}
			newer = append(newer, u)
		}
		filter.Offset += len(page.Upgrades)
		if len(page.Upgrades) == 0 || filter.Offset >= page.Total {
			return nil, nil
		}
	}
}
