package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/soroworks/sorovault/internal/stellar"
	"github.com/soroworks/sorovault/internal/store"
)

// getStats serves GET /api/stats.
func (s *Server) getStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.reg.Store().Stats(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}

	resp := statsResponse{
		Networks:       stats.Networks,
		Contracts:      stats.Contracts,
		Versions:       stats.Versions,
		LatestContract: stats.Latest,
		ReadOnly:       s.readOnly,
	}
	if s.lastSweep != nil {
		if record, ok := s.lastSweep(); ok {
			resp.LastRefresh = &record
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// upgradeFilter reads the paging and network parameters shared by the JSON
// and Atom forms of the upgrade feed.
func upgradeFilter(r *http.Request) store.UpgradeFilter {
	filter := store.UpgradeFilter{
		Network: r.URL.Query().Get("network"),
		Limit:   intParam(r.URL.Query().Get("limit"), 0),
		Offset:  intParam(r.URL.Query().Get("offset"), 0),
	}
	filter.Normalize()
	return filter
}

// listUpgrades serves GET /api/upgrades.
func (s *Server) listUpgrades(w http.ResponseWriter, r *http.Request) {
	filter := upgradeFilter(r)

	page, err := s.reg.Store().ListUpgrades(r.Context(), filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, upgradeListResponse{
		Upgrades: page.Upgrades,
		Total:    page.Total,
		Limit:    filter.Limit,
		Offset:   filter.Offset,
	})
}

// subscribeRequest is the request body for creating a subscription.
type subscribeRequest struct {
	ContractIDs []string `json:"contract_ids"`
	URL         string   `json:"url"`
}

// subscribeResponse is the response for subscription operations.
type subscribeResponse struct {
	ID        string `json:"id"`
	Network   string `json:"network"`
	Contracts int    `json:"contracts"`
	Active    bool   `json:"active"`
}

// subscriptionListResponse is the response for listing subscriptions.
type subscriptionListResponse struct {
	Subscriptions []store.SubscriptionListItem `json:"subscriptions"`
}

// subscribeContract serves POST /api/subscriptions.
// Registers a webhook URL for a set of contracts, returning a secret used
// to sign deliveries.
func (s *Server) subscribeContract(w http.ResponseWriter, r *http.Request) {
	var req subscribeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateSubscription(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	net, _ := s.network(r)
	secret, err := s.reg.Store().Subscribe(r.Context(), net, req.ContractIDs, req.URL)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, subscribeResponse{
		ID:        secret,
		Network:   net,
		Contracts: len(req.ContractIDs),
		Active:    true,
	})
}

// listSubscriptions serves GET /api/subscriptions.
// Lists all subscriptions.
func (s *Server) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	items, err := s.reg.Store().ListSubscriptions(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// A subscription's ID is the secret that signs its deliveries and
	// deletes it, so a public listing must not reveal it.
	for i := range items {
		items[i].ID = ""
	}

	writeJSON(w, http.StatusOK, subscriptionListResponse{
		Subscriptions: items,
	})
}

// unsubscribeContract serves DELETE /api/subscriptions.
// Removes a subscription by its secret.
func (s *Server) unsubscribeContract(w http.ResponseWriter, r *http.Request) {
	net, _ := s.network(r)
	secret := chi.URLParam(r, "secret")
	if secret == "" {
		writeError(w, http.StatusBadRequest, "secret is required")
		return
	}

	err := s.reg.Store().Unsubscribe(r.Context(), secret)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, subscribeResponse{
		ID:        secret,
		Network:   net,
		Contracts: 0,
		Active:    false,
	})
}

// maxSubscriptionContracts bounds how many contracts one subscription covers.
const maxSubscriptionContracts = 20

// validateSubscription checks a subscription request before anything is
// stored: at least one well-formed contract ID, and an absolute HTTP(S)
// webhook URL.
func validateSubscription(req subscribeRequest) error {
	if len(req.ContractIDs) == 0 {
		return errors.New("contract_ids must name at least one contract")
	}
	if len(req.ContractIDs) > maxSubscriptionContracts {
		return fmt.Errorf("contract_ids may name at most %d contracts", maxSubscriptionContracts)
	}
	for _, id := range req.ContractIDs {
		if err := stellar.ValidateContractID(id); err != nil {
			return err
		}
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("url must be an absolute http or https URL")
	}
	return nil
}
