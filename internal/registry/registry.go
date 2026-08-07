// Package registry joins the three moving parts of SoroVault: it fetches a
// contract's code from a network, decodes its interface, and records the
// result.
//
// It holds the rules that do not belong to any one of those layers — how a
// network is resolved and verified, when a refresh counts as a change, and
// what "already registered" means — so that the API, the CLI and the browse
// UI all behave identically.
package registry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/stellar"
	"github.com/soroworks/sorovault/internal/store"
)

// ErrNetworkMismatch reports that the RPC endpoint serves a different
// network than the one configured. Continuing would file records under the
// wrong namespace, so registration stops instead.
var ErrNetworkMismatch = errors.New("registry: RPC endpoint serves a different network than configured")

// Registry registers and refreshes contracts.
type Registry struct {
	fetcher stellar.Fetcher
	decoder spec.Decoder
	store   store.Store
	log     *slog.Logger

	// expectPassphrase, when set, must match what the endpoint reports.
	expectPassphrase string

	// The network label is resolved from the endpoint on first use and
	// cached; it does not change for the life of the process. A mismatch is
	// cached too, since retrying cannot fix a misconfiguration, but a
	// transient RPC failure is not.
	networkMu       sync.Mutex
	network         string
	networkMismatch error
}

// Options configures a Registry.
type Options struct {
	// ExpectPassphrase is the network the operator believes RPC serves. If
	// non-empty and the endpoint disagrees, calls fail with
	// ErrNetworkMismatch rather than storing under the wrong network.
	ExpectPassphrase string
	// Logger receives operational detail. Defaults to slog.Default.
	Logger *slog.Logger
}

// New builds a Registry from its three collaborators.
func New(f stellar.Fetcher, d spec.Decoder, s store.Store, opts Options) *Registry {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Registry{
		fetcher:          f,
		decoder:          d,
		store:            s,
		log:              log,
		expectPassphrase: opts.ExpectPassphrase,
	}
}

// Result describes what a register or refresh call did.
type Result struct {
	Contract  store.Contract   `json:"contract"`
	Interface *model.Interface `json:"interface,omitempty"`

	// Created is true when this call added a contract the registry had not
	// seen before.
	Created bool `json:"created"`
	// Changed is true when the contract's WASM hash differed from what was
	// stored, so a new interface version was recorded.
	Changed bool `json:"changed"`
}

// Network returns the label records are stored under, resolved from the RPC
// endpoint on first call and cached thereafter.
func (r *Registry) Network(ctx context.Context) (string, error) {
	r.networkMu.Lock()
	defer r.networkMu.Unlock()

	if r.networkMismatch != nil {
		return "", r.networkMismatch
	}
	if r.network != "" {
		return r.network, nil
	}

	passphrase, err := r.fetcher.Network(ctx)
	if err != nil {
		// Left uncached: a blip on the first call must not wedge the
		// process into permanently refusing to resolve its network.
		return "", err
	}

	if r.expectPassphrase != "" && passphrase != r.expectPassphrase {
		r.networkMismatch = fmt.Errorf("%w: endpoint reports %q, configured %q",
			ErrNetworkMismatch, passphrase, r.expectPassphrase)
		return "", r.networkMismatch
	}

	r.network = stellar.NetworkName(passphrase)
	return r.network, nil
}

// Register fetches, decodes and stores a contract's interface.
//
// It is safe to call on a contract that is already registered: an unchanged
// contract is simply re-verified and its last_refreshed bumped, which makes
// the operation idempotent and lets it double as an explicit refresh.
func (r *Registry) Register(ctx context.Context, contractID string) (*Result, error) {
	if err := stellar.ValidateContractID(contractID); err != nil {
		return nil, err
	}

	network, err := r.Network(ctx)
	if err != nil {
		return nil, err
	}

	// Whether the contract is new decides Created, and it has to be read
	// before Save overwrites the answer.
	existing, err := r.store.GetContract(ctx, network, contractID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	contract, err := r.fetcher.FetchContract(ctx, contractID)
	if err != nil {
		return nil, err
	}

	iface, err := r.decoder.Decode(contract.Wasm)
	if err != nil {
		return nil, fmt.Errorf("registry: decoding %s: %w", contractID, err)
	}

	record := store.Contract{
		ContractID:      contractID,
		Network:         network,
		CurrentWasmHash: contract.WasmHash,
	}
	if err := r.store.Save(ctx, record, iface); err != nil {
		return nil, err
	}

	saved, err := r.store.GetContract(ctx, network, contractID)
	if err != nil {
		return nil, err
	}

	result := &Result{
		Contract:  *saved,
		Interface: iface,
		Created:   existing == nil,
		Changed:   existing == nil || existing.CurrentWasmHash != contract.WasmHash,
	}

	r.log.InfoContext(ctx, "registered contract",
		"contract_id", contractID,
		"network", network,
		"wasm_hash", contract.WasmHash,
		"created", result.Created,
		"changed", result.Changed,
		"functions", len(iface.Functions),
	)

	return result, nil
}

// Refresh re-checks a registered contract against the network and records a
// new interface version if its WASM has changed.
//
// Unlike Register it starts from what is already stored, so it can settle an
// unchanged contract with a single RPC round trip: the module is only
// downloaded once the hash is known to differ.
func (r *Registry) Refresh(ctx context.Context, contractID string) (*Result, error) {
	if err := stellar.ValidateContractID(contractID); err != nil {
		return nil, err
	}

	network, err := r.Network(ctx)
	if err != nil {
		return nil, err
	}

	existing, err := r.store.GetContract(ctx, network, contractID)
	if err != nil {
		return nil, err
	}

	currentHash, err := r.fetcher.WasmHash(ctx, contractID)
	if err != nil {
		return nil, err
	}

	if currentHash == existing.CurrentWasmHash {
		// Nothing new to decode, but the contract was just confirmed live,
		// so re-save to advance last_refreshed. Save is a no-op on the spec
		// row when the hash is unchanged.
		stored, err := r.store.GetSpec(ctx, network, contractID, currentHash)
		if err != nil {
			return nil, err
		}
		if err := r.store.Save(ctx, *existing, stored.Interface); err != nil {
			return nil, err
		}
		updated, err := r.store.GetContract(ctx, network, contractID)
		if err != nil {
			return nil, err
		}

		r.log.DebugContext(ctx, "contract unchanged",
			"contract_id", contractID, "network", network, "wasm_hash", currentHash)

		return &Result{Contract: *updated, Interface: stored.Interface}, nil
	}

	contract, err := r.fetcher.FetchContract(ctx, contractID)
	if err != nil {
		return nil, err
	}

	iface, err := r.decoder.Decode(contract.Wasm)
	if err != nil {
		return nil, fmt.Errorf("registry: decoding %s: %w", contractID, err)
	}

	record := *existing
	record.CurrentWasmHash = contract.WasmHash
	if err := r.store.Save(ctx, record, iface); err != nil {
		return nil, err
	}

	updated, err := r.store.GetContract(ctx, network, contractID)
	if err != nil {
		return nil, err
	}

	r.log.InfoContext(ctx, "contract upgraded",
		"contract_id", contractID,
		"network", network,
		"from_wasm_hash", existing.CurrentWasmHash,
		"to_wasm_hash", contract.WasmHash,
	)

	return &Result{Contract: *updated, Interface: iface, Changed: true}, nil
}

// Store exposes the underlying store for read-only queries the API and UI
// serve directly, so they do not each need their own handle.
func (r *Registry) Store() store.Store { return r.store }
