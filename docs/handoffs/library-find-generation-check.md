# Handoff: stop text search failing when its overlap check is slow

## Assignment

Library Find and the text search API can answer HTTP 500 with
`context deadline exceeded` on a busy machine, even though the search itself
succeeded. Make a slow overlap check degrade gracefully. Keep the guarantee
that one page never mixes two versions of the messages.

## Evidence

- **Where it fails.** After reading a page, `SearchText` (`internal/archive/text_search.go`, around line 725) re-reads the message generation to detect writes that overlapped the page. That check runs under a hard-coded `context.WithTimeout(parent, 100*time.Millisecond)`. If the check misses the deadline, the whole search returns the context error, and the HTTP handlers report it as a 500.
- **When it was seen.** On 2026-10-19, `TestTextSearchVisualLibraryRequests` failed with `visual query ignored: 500 {"error":"context deadline exceeded"}`. Three full Go suites were running side by side, at load average about 70 on 28 cores.
  - Alone, it passed 20 of 20 runs even at load about 80.
  - The failing test took 0.23 s in total, well inside the search's own 1.5 s budget. Only the 100 ms check ran out.
- **Who can hit it.** A user can hit this while indexing or a backup keeps the Mac busy. It came in with #109, which added bounded text search.

## Where to look

- `internal/archive/text_search.go`: `SearchText`, `textSearchGeneration`, the `TimeoutMS` budget (default 1,500 ms, clamped 1–5,000), and the page and stop-reason fields (`StopReason`, `Complete`, `CountRelation`, `NextCursor`).
- Callers: Library Find (`conversation_search.go`, used by `library_find_filter.go`), `/api/search/text` (`api.go`) and the MCP `search_text` tool (`text_search_api.go`).
- Existing hooks for testing stalls: `hookTextSearch` in `text_search_cursor_test.go`.

## Constraints

- **Keep the guarantee.** A page must never silently mix source versions; that is why the check exists.
- **Pick a sensible failure.** A check that cannot finish should give either:
  - a clear, retryable outcome, such as the existing "messages changed during search; restart the search" error, or a stop reason with no cursor; or
  - a check bounded by what is left of the search's own time budget instead of a fixed 100 ms.

  It should never give a 500. Choose the behaviour that fits the stop-reason contract documented for `search_text`, and update the docs in `text_search_api.go` if the contract changes.
- **Make the bound testable.** If you keep a fixed bound, make it a package variable so a test can lower it with `override`.

## Done when

- A Go test stalls the generation check, using a hook or a lowered bound, and asserts the search does not return a 500. It must also assert that the caller gets the chosen outcome. Use no real sleeps beyond the bound you set.
- `go test ./...` passes.
- The overlap test that already exists still fails a page when the generation really changes.
