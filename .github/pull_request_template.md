## What this changes

<!-- One or two sentences. Link the issue: "Closes #123". -->

<!-- Example:
Closes #123. Adds a prose paragraph below the README configuration table
explaining `HTTP_ADDR`'s default (`:8080` binds all interfaces), an example
loopback override (`127.0.0.1:8080`), and the `--addr` flag. Facts verified
against `internal/config/config.go` and `.env.example`.
-->

## How it was tested

<!-- The tests you added, and anything you ran by hand. -->
<!-- Example: `go test ./internal/mcp` -->

<!-- Example:
- `make check` passes (`gofmt`, `go vet`, unit tests).
- `go test ./internal/config/...` — new `TestLoadHTTPAddr` covers the default
  and loopback cases added in this change.
- Docs-only sections: verified table still renders and the new paragraph is
  reachable from the section anchor.
-->

## Checklist

<!-- Example: [x] `make check` passes locally -->
- [ ] `make check` passes locally (gofmt, vet, tests)
- [ ] New behaviour has tests that need neither a network nor a database
- [ ] README / `.env.example` updated if a flag, variable or output changed
