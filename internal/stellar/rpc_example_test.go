package stellar_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"

	"github.com/soroworks/sorovault/internal/stellar"
)

func ExampleNewRPCFetcher() {
	// Use a local RPC response so the example does not need a network connection.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      request.ID,
			"result": map[string]any{
				"passphrase":      stellar.NetworkTestnet,
				"protocolVersion": 23,
			},
		})
	}))
	defer server.Close()

	fetcher := stellar.NewRPCFetcher(server.URL, server.Client())
	defer fetcher.Close()

	network, err := fetcher.Network(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(network)
	// Output: Test SDF Network ; September 2015
}
