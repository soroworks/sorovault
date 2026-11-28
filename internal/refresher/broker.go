package refresher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/soroworks/sorovault/internal/store"
)

// ErrStreamFull reports that the upgrade SSE broker is already serving the
// configured maximum number of connected clients.
var ErrStreamFull = errors.New("refresher: too many active upgrade stream clients")

// Defaults for the in-process upgrade event broker.
const (
	DefaultStreamClients = 64
	DefaultStreamBuffer  = 16
)

// BrokerOptions configures a Broker.
type BrokerOptions struct {
	// MaxClients bounds how many SSE clients may stay connected at once.
	// Values below one use DefaultStreamClients.
	MaxClients int
	// BufferSize is how many unread upgrades a client may queue before the
	// broker drops it as a slow consumer. Values below one use
	// DefaultStreamBuffer.
	BufferSize int
}

type subscriber struct {
	network string
	ch      chan store.Upgrade
}

// Broker fans interface upgrades out to connected server-sent event clients.
//
// Publishing never blocks: a client whose buffer is full is dropped and its
// channel closed so a slow or stalled reader cannot hold up a refresh sweep.
type Broker struct {
	mu         sync.Mutex
	clients    map[*subscriber]struct{}
	maxClients int
	bufferSize int
}

var _ Notifier = (*Broker)(nil)

// NewBroker builds an in-process upgrade broker.
func NewBroker(opts BrokerOptions) *Broker {
	maxClients := opts.MaxClients
	if maxClients < 1 {
		maxClients = DefaultStreamClients
	}
	bufferSize := opts.BufferSize
	if bufferSize < 1 {
		bufferSize = DefaultStreamBuffer
	}
	return &Broker{
		clients:    make(map[*subscriber]struct{}),
		maxClients: maxClients,
		bufferSize: bufferSize,
	}
}

// SetMaxClients updates the connection limit for subsequent subscriptions.
func (b *Broker) SetMaxClients(maxClients int) {
	if maxClients < 1 {
		maxClients = DefaultStreamClients
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maxClients = maxClients
}

// SetBufferSize updates the per-client channel buffer for subsequent
// subscriptions.
func (b *Broker) SetBufferSize(bufferSize int) {
	if bufferSize < 1 {
		bufferSize = DefaultStreamBuffer
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bufferSize = bufferSize
}

// Subscribe registers a listener for upgrades on network (or all networks when
// network is empty). It returns ErrStreamFull when the connection limit has
// been reached. The returned channel is closed when unsubscribe is called or
// when the broker drops the client for falling behind.
func (b *Broker) Subscribe(network string) (<-chan store.Upgrade, func(), error) {
	network = strings.TrimSpace(network)

	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.clients) >= b.maxClients {
		return nil, nil, fmt.Errorf("%w: limit is %d", ErrStreamFull, b.maxClients)
	}

	sub := &subscriber{
		network: network,
		ch:      make(chan store.Upgrade, b.bufferSize),
	}
	b.clients[sub] = struct{}{}

	unsubscribe := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.clients[sub]; ok {
			delete(b.clients, sub)
			close(sub.ch)
		}
	}
	return sub.ch, unsubscribe, nil
}

// Publish sends u to every subscribed client whose network filter matches.
// Clients whose buffer is full are dropped and their channels closed.
func (b *Broker) Publish(u store.Upgrade) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for sub := range b.clients {
		if sub.network != "" && sub.network != u.Network {
			continue
		}
		select {
		case sub.ch <- u:
		default:
			delete(b.clients, sub)
			close(sub.ch)
		}
	}
}

// Notify satisfies Notifier by publishing the change as an Upgrade.
func (b *Broker) Notify(_ context.Context, change Change) error {
	b.Publish(store.Upgrade{
		ContractID:  change.ContractID,
		Network:     change.Network,
		OldWasmHash: change.OldWasmHash,
		NewWasmHash: change.NewWasmHash,
		ObservedAt:  change.ChangedAt,
	})
	return nil
}

// UpgradeEventID formats the stable event identifier for an upgrade, matching
// the Atom feed's entry ID.
func UpgradeEventID(u store.Upgrade) string {
	return fmt.Sprintf("urn:sorovault:upgrade:%s:%s:%s", u.Network, u.ContractID, u.NewWasmHash)
}

// MatchesUpgradeEventID reports whether id identifies u, accepting either the
// full URN ("urn:sorovault:upgrade:<network>:<contract>:<hash>") or the
// compact "<network>:<contract>:<hash>" form.
func MatchesUpgradeEventID(u store.Upgrade, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if id == UpgradeEventID(u) {
		return true
	}
	return id == fmt.Sprintf("%s:%s:%s", u.Network, u.ContractID, u.NewWasmHash)
}
