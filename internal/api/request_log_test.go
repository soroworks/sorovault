package api_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/api"
)

// The request log is the only record of who registered what, so its fields
// are part of the API's contract with whoever reads the log later. These
// tests decode each line as JSON and assert on the fields, rather than
// grepping the raw line for substrings: a substring assertion would pass on
// a message that merely mentions a status, not on a structured one that a
// log pipeline can filter on.
//
// Which path is logged
//
// The logged path is the route pattern — /api/contracts/{id} — not the raw
// request path. The raw path of a registration carries the contract ID, so
// logging it would name a specific registration rather than the endpoint
// that served it: one line per contract instead of one line per route, an
// unbounded log, and an identifier in the log that the route pattern already
// describes. Tests below assert the pattern is logged and that the contract
// ID is not. When nothing matched at all, chi leaves the pattern empty and
// the raw path is logged instead, because there is no route to name.

// contractID is deliberately realistic: it is the value the raw path would
// carry if the logger were logging r.URL.Path.
const requestLogContractID = "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN"

// logCapture is a logger writing structured lines into a buffer, so the test
// can decode what requestLogger actually emitted.
type logCapture struct {
	buf bytes.Buffer
	log *slog.Logger
}

// newLogCapture builds a debug-level JSON logger over a buffer. Debug is the
// level requestLogger writes successes at, so the handler's own line is
// captured rather than filtered out.
func newLogCapture() *logCapture {
	c := &logCapture{}
	c.log = slog.New(slog.NewJSONHandler(&c.buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return c
}

// entries decodes every line written so far, failing the test on a line that
// is not JSON — a logger that emitted an unparsable line would break every
// consumer downstream.
func (c *logCapture) entries(t *testing.T) []map[string]any {
	t.Helper()

	var out []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(c.buf.Bytes()))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry), "log line is not JSON: %s", line)
		out = append(out, entry)
	}
	require.NoError(t, scanner.Err())
	return out
}

// requestLogRouter wires the middleware the way serve does — a root router
// carrying the middleware, with the API mounted beneath it — so the route
// pattern recorded is the mounted one, /api/contracts/{id}.
func requestLogRouter(log *slog.Logger) http.Handler {
	root := chi.NewRouter()
	root.Use(api.Middleware(log)...)

	apiRouter := chi.NewRouter()
	// The body is fixed: what these tests care about is the status and the
	// bytes written, and echoing the path back would put the very ID the
	// log must not carry into the response.
	apiRouter.Get("/contracts/{id}", func(w http.ResponseWriter, _ *http.Request) {
		// Keep elapsed time above zero even on platforms whose monotonic
		// clock has coarse resolution (notably Windows).
		time.Sleep(time.Millisecond)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"contract_id":"logged"}`+"\n")
	})
	root.Mount("/api", apiRouter)

	return root
}

// TestRequestLoggerRecordsASuccessfulRequest asserts the line a successful
// request produces carries every field the log is meant to be queried by:
// method, route, status, duration, bytes and request ID.
func TestRequestLoggerRecordsASuccessfulRequest(t *testing.T) {
	t.Parallel()

	c := newLogCapture()
	router := requestLogRouter(c.log)

	req := httptest.NewRequest(http.MethodGet, "/api/contracts/"+requestLogContractID, nil)
	// chi's request-ID middleware adopts an ID the client already sent, so
	// the test pins one: request_id is then exactly what the log must carry.
	req.Header.Set("X-Request-Id", "req-for-the-log")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	entries := c.entries(t)
	require.Len(t, entries, 1, "one request must produce exactly one log line")
	entry := entries[0]

	assert.Equal(t, "request", entry["msg"])
	assert.Equal(t, "DEBUG", entry["level"], "a successful request is debug, not error")
	assert.Equal(t, http.MethodGet, entry["method"])
	assert.EqualValues(t, http.StatusOK, entry["status"])
	assert.Equal(t, "/api/contracts/{id}", entry["route"],
		"the route pattern must be logged, not the raw path with a contract ID in it")
	assert.NotContains(t, entry["route"], requestLogContractID)

	// Duration is a time.Duration, which the JSON handler writes as whole
	// nanoseconds; asserting on the decoded number keeps the test honest
	// about the unit rather than about a formatted string.
	duration, ok := entry["duration"].(float64)
	require.True(t, ok, "duration must be a number of nanoseconds, got %T", entry["duration"])
	assert.Greater(t, duration, float64(0), "a request takes time")

	bytesWritten, ok := entry["bytes"].(float64)
	require.True(t, ok, "bytes must be a number, got %T", entry["bytes"])
	assert.Greater(t, bytesWritten, float64(0))

	// request_id ties the line back to the exchange that produced it: a
	// client that sent an ID gets its own ID, so a support ticket naming one
	// can be grepped straight out of the log.
	requestID, ok := entry["request_id"].(string)
	require.True(t, ok, "request_id must be a string, got %T", entry["request_id"])
	assert.Equal(t, "req-for-the-log", requestID)
}

// TestRequestLoggerRecordsAFailedRequest asserts a request that matched no
// route still logs its status: 404s are the failures an operator looks for,
// and a logger that dropped them would be useless.
func TestRequestLoggerRecordsAFailedRequest(t *testing.T) {
	t.Parallel()

	c := newLogCapture()
	router := requestLogRouter(c.log)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/contracts/"+requestLogContractID+"/nope", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)

	entries := c.entries(t)
	require.Len(t, entries, 1)
	entry := entries[0]

	assert.Equal(t, "request", entry["msg"])
	assert.EqualValues(t, http.StatusNotFound, entry["status"], "a 404 must be logged with its status")
	assert.Equal(t, http.MethodGet, entry["method"])
	assert.NotContains(t, entry["route"], requestLogContractID,
		"even a 404 under a contract path must not log the contract ID")

	// No ID was sent with this one, so the logger minted one: the field is
	// always present, never empty.
	requestID, ok := entry["request_id"].(string)
	require.True(t, ok, "request_id must be a string, got %T", entry["request_id"])
	assert.NotEmpty(t, requestID)
}

// TestRequestLoggerLogsTheRoutePattern pins the documented choice of path:
// the pattern for a matched route, and chi's mount pattern when no route
// matched — never the raw path. See the comment at the top of this file for
// why.
func TestRequestLoggerLogsTheRoutePattern(t *testing.T) {
	t.Parallel()

	c := newLogCapture()
	router := requestLogRouter(c.log)

	for _, path := range []string{
		"/api/contracts/" + requestLogContractID,
		"/api/contracts/" + requestLogContractID + "/nope",
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.NotEmpty(t, rec.Body)
	}

	entries := c.entries(t)
	require.Len(t, entries, 2)
	for _, entry := range entries {
		route, ok := entry["route"].(string)
		require.True(t, ok, "route must be a string, got %T", entry["route"])
		assert.NotContains(t, route, requestLogContractID)
		assert.NotEqual(t, "/api/contracts/"+requestLogContractID, route)
	}
	assert.Equal(t, "/api/contracts/{id}", entries[0]["route"])
	// Nothing under /api matched, so chi reports the mount rather than the
	// path the client asked for.
	assert.Equal(t, "/api/*", entries[1]["route"])
}
