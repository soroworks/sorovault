package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/refresher"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
	"github.com/soroworks/sorovault/internal/store"
)

// upgradeFixture registers one contract and upgrades it twice, with a clock
// that advances on every store write so the events order deterministically.
func upgradeFixture(t *testing.T) (*harness, []string) {
	t.Helper()

	memory := store.NewMemory()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	memory.SetClock(func() time.Time {
		now = now.Add(time.Minute)
		return now
	})
	h := newHarnessWithStore(t, stellartest.New(), memory)

	v1 := moduleWith(t, "v1")
	h.fake.Deploy(idA, v1)
	resp, raw := h.do(t, http.MethodPost, "/api/contracts", `{"contract_id":"`+idA+`"}`)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(raw))

	v2 := moduleWith(t, "v2")
	h.fake.Upgrade(idA, v2)
	resp, raw = h.do(t, http.MethodPost, "/api/contracts/"+idA+"/refresh", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

	v3 := moduleWith(t, "v3")
	h.fake.Upgrade(idA, v3)
	resp, raw = h.do(t, http.MethodPost, "/api/contracts/"+idA+"/refresh", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

	return h, []string{stellartest.HashOf(v1), stellartest.HashOf(v2), stellartest.HashOf(v3)}
}

func TestUpgradesFeed(t *testing.T) {
	t.Parallel()

	h, hashes := upgradeFixture(t)

	t.Run("first registration is not an upgrade", func(t *testing.T) {
		// The fixture has two upgrades, not three: the initial version is
		// how the contract entered the registry.
		resp, raw := h.do(t, http.MethodGet, "/api/upgrades", "")
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

		got := h.decode(t, raw)
		assert.Equal(t, float64(2), got["total"])
		assert.Len(t, got["upgrades"].([]any), 2)
	})

	t.Run("newest first with both hashes", func(t *testing.T) {
		resp, raw := h.do(t, http.MethodGet, "/api/upgrades", "")
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

		upgrades := h.decode(t, raw)["upgrades"].([]any)
		require.Len(t, upgrades, 2)

		newest := upgrades[0].(map[string]any)
		assert.Equal(t, idA, newest["contract_id"])
		assert.Equal(t, "testnet", newest["network"])
		assert.Equal(t, hashes[1], newest["old_wasm_hash"])
		assert.Equal(t, hashes[2], newest["new_wasm_hash"])
		assert.NotEmpty(t, newest["observed_at"])

		older := upgrades[1].(map[string]any)
		assert.Equal(t, hashes[0], older["old_wasm_hash"])
		assert.Equal(t, hashes[1], older["new_wasm_hash"])
	})

	t.Run("paging", func(t *testing.T) {
		resp, raw := h.do(t, http.MethodGet, "/api/upgrades?limit=1&offset=1", "")
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

		got := h.decode(t, raw)
		assert.Equal(t, float64(2), got["total"], "total ignores paging")
		assert.Equal(t, float64(1), got["limit"])
		assert.Equal(t, float64(1), got["offset"])

		upgrades := got["upgrades"].([]any)
		require.Len(t, upgrades, 1)
		assert.Equal(t, hashes[1], upgrades[0].(map[string]any)["new_wasm_hash"],
			"the second page holds the older upgrade")
	})

	t.Run("past the end still reports the total", func(t *testing.T) {
		resp, raw := h.do(t, http.MethodGet, "/api/upgrades?offset=99", "")
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

		got := h.decode(t, raw)
		assert.Equal(t, []any{}, got["upgrades"])
		assert.Equal(t, float64(2), got["total"])
	})

	t.Run("network filter", func(t *testing.T) {
		resp, raw := h.do(t, http.MethodGet, "/api/upgrades?network=public", "")
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

		got := h.decode(t, raw)
		assert.Equal(t, float64(0), got["total"])
		assert.Equal(t, []any{}, got["upgrades"])
	})
}

// TestUpgradesAtomFeed proves the Atom form carries the same events with the
// fields a feed reader needs, and that paging is linked.
func TestUpgradesAtomFeed(t *testing.T) {
	t.Parallel()

	h, hashes := upgradeFixture(t)

	type atomDocument struct {
		ID      string `xml:"id"`
		Title   string `xml:"title"`
		Updated string `xml:"updated"`
		Links   []struct {
			Rel  string `xml:"rel,attr"`
			Href string `xml:"href,attr"`
		} `xml:"link"`
		Entries []struct {
			ID      string `xml:"id"`
			Title   string `xml:"title"`
			Updated string `xml:"updated"`
			Summary string `xml:"summary"`
			Link    struct {
				Href string `xml:"href,attr"`
			} `xml:"link"`
		} `xml:"entry"`
	}

	resp, raw := h.do(t, http.MethodGet, "/api/upgrades.atom", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/atom+xml")

	var feed atomDocument
	require.NoError(t, xml.Unmarshal(raw, &feed), "the feed must be well-formed Atom XML")

	assert.Equal(t, "urn:sorovault:upgrades", feed.ID)
	assert.NotEmpty(t, feed.Updated)
	require.Len(t, feed.Entries, 2)

	newest := feed.Entries[0]
	assert.Contains(t, newest.Title, idA)
	assert.Contains(t, newest.Summary, hashes[1])
	assert.Contains(t, newest.Summary, hashes[2])
	assert.NotEmpty(t, newest.Updated)
	_, err := time.Parse(time.RFC3339, newest.Updated)
	assert.NoError(t, err, "Atom requires an RFC 3339 timestamp")
	assert.Contains(t, newest.Link.Href, "/api/contracts/"+idA)

	assert.Contains(t, feed.Entries[1].Summary, hashes[0],
		"entries are newest first, like the JSON feed")

	resp, raw = h.do(t, http.MethodGet, "/api/upgrades.atom?limit=1", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))

	feed = atomDocument{}
	require.NoError(t, xml.Unmarshal(raw, &feed))
	require.Len(t, feed.Entries, 1)

	var next string
	for _, link := range feed.Links {
		if link.Rel == "next" {
			next = link.Href
		}
	}
	assert.Contains(t, next, "offset=1", "a page with more events links to the next one")
}

type sseEvent struct {
	id    string
	event string
	data  string
}

func readSSEEvent(t *testing.T, reader *bufio.Reader) sseEvent {
	t.Helper()
	var ev sseEvent
	for {
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, ":") {
			continue
		}
		if line == "" {
			if ev.id != "" || ev.event != "" || ev.data != "" {
				return ev
			}
			continue
		}
		if v, ok := strings.CutPrefix(line, "id: "); ok {
			ev.id = v
		} else if v, ok := strings.CutPrefix(line, "event: "); ok {
			ev.event = v
		} else if v, ok := strings.CutPrefix(line, "data: "); ok {
			ev.data = v
		}
	}
}

func TestUpgradesSSEStreamAndReplay(t *testing.T) {
	t.Parallel()

	h, hashes := upgradeFixture(t)

	// Reconnect with Last-Event-ID set to the first upgrade (hashes[0] -> hashes[1]);
	// the stream should immediately replay the second upgrade (hashes[1] -> hashes[2])
	// and then deliver live upgrades as the refresher records them.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	firstUpgradeID := refresher.UpgradeEventID(store.Upgrade{
		Network:     "testnet",
		ContractID:  idA,
		NewWasmHash: hashes[1],
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.server.URL+"/api/upgrades/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Last-Event-ID", firstUpgradeID)

	resp, err := h.server.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")

	reader := bufio.NewReader(resp.Body)

	// 1. Missed upgrade is replayed from the feed.
	replayed := readSSEEvent(t, reader)
	assert.Equal(t, "upgrade", replayed.event)
	var u1 store.Upgrade
	require.NoError(t, json.Unmarshal([]byte(replayed.data), &u1))
	assert.Equal(t, hashes[1], u1.OldWasmHash)
	assert.Equal(t, hashes[2], u1.NewWasmHash)

	// 2. Live upgrade published by the refresher is streamed immediately.
	v4 := moduleWith(t, "v4")
	hash4 := stellartest.HashOf(v4)
	h.api.UpgradeBroker().Publish(store.Upgrade{
		ContractID:  idA,
		Network:     "testnet",
		OldWasmHash: hashes[2],
		NewWasmHash: hash4,
		ObservedAt:  time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	})

	live := readSSEEvent(t, reader)
	assert.Equal(t, "upgrade", live.event)
	var u2 store.Upgrade
	require.NoError(t, json.Unmarshal([]byte(live.data), &u2))
	assert.Equal(t, hashes[2], u2.OldWasmHash)
	assert.Equal(t, hash4, u2.NewWasmHash)
}

func TestUpgradesSSEConnectionLimitsAndSlowClients(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.api.UpgradeBroker().SetMaxClients(1)
	h.api.UpgradeBroker().SetBufferSize(1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req1, err := http.NewRequestWithContext(ctx, http.MethodGet, h.server.URL+"/api/upgrades/stream", nil)
	require.NoError(t, err)
	resp1, err := h.server.Client().Do(req1)
	require.NoError(t, err)
	defer func() { _ = resp1.Body.Close() }()
	require.Equal(t, http.StatusOK, resp1.StatusCode)

	// A second concurrent client beyond MaxClients=1 is refused with 503.
	resp2, raw2 := h.do(t, http.MethodGet, "/api/upgrades/stream", "")
	assert.Equal(t, http.StatusServiceUnavailable, resp2.StatusCode, string(raw2))

	// Overflowing the first client's buffer drops the slow client cleanly.
	for i := 0; i < 4; i++ {
		h.api.UpgradeBroker().Publish(store.Upgrade{
			ContractID:  idA,
			Network:     "testnet",
			OldWasmHash: "h1",
			NewWasmHash: "h2",
			ObservedAt:  time.Now().UTC(),
		})
	}

	// After the slow client is dropped, a new client can connect again.
	ctx3, cancel3 := context.WithCancel(context.Background())
	defer cancel3()
	req3, err := http.NewRequestWithContext(ctx3, http.MethodGet, h.server.URL+"/api/upgrades/stream", nil)
	require.NoError(t, err)
	resp3, err := h.server.Client().Do(req3)
	require.NoError(t, err)
	defer func() { _ = resp3.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp3.StatusCode)
}
