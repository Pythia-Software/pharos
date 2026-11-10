# Result cache

The result cache is a small SQLite file that holds finished responses in the
form the frontend receives them. A page can be drawn from it without the
catalog being read at all, which is what keeps pages responsive on first load
and while the service is busy.

It is separate from the catalog on purpose. The in-memory caches are empty
after a restart, and a cache stored in the catalog still needs a catalog
connection, which is the resource that runs out under load.

- Code: `internal/archive/result_cache.go` (the mechanism, all of it),
  `internal/archive/query_result_cache.go` (the first domain),
  `web/src/result-cache.ts` (the frontend's rules).
- File: `<support dir>/result-cache/<hash of the library's identity>.sqlite3`,
  on this Mac rather than the library's drive. Deleting it is always safe.

## One file per library

The cache is part of a library, even though it is not stored with it. Each
library has its own file, named for the library's id and instance from its
descriptor (`resultCacheIdentity`):

- Two libraries never share a file, including two that are opened at the
  same path at different times (one drive name, different drives).
- A library keeps its file when its drive is mounted somewhere else.
- A restored library is a new instance, so it starts with an empty cache
  rather than answers computed from the catalog it replaced.
- The file records whose it is. If a file is ever found holding another
  library's entries, they are deleted before anything is served.
- A library that cannot be identified (its descriptor is unreadable or says
  it is a copy) gets no cache at all: every request is computed.

A catalog that is not a library, as in development and tests, is known by
its path instead.

## The rules

**1. Only a request that names a policy uses the cache.** The request header
`X-Pharos-Cache` is `cached`, `fresh` or `live`. Without it a request is
computed as before and nothing is stored.

**2. An entry is one request's response.** Its key is the request without its
clock, so every page load of one card is the same entry. Each entry records
the *stamp* of the data it was computed from and when it was computed.

**3. An entry is added whenever a response is computed under a policy.** It
is written after the response has been sent. Errors are never stored. A
request whose browser gave up before its answer was computed is remembered
and computed in the background, so the next load of that view has an answer
however long its query takes.

**4. An entry is *fresh* while its stamp is the current one**, and, if its
request reads the clock (a window ending now), while it is younger than the
domain's maximum age. Otherwise it is *stale*. Stale entries are kept and can
still be served; nothing is deleted when data changes.

**5. The policy decides between the stored answer and a live one:**

| Stored entry | `cached` | `fresh` | `live` |
|---|---|---|---|
| fresh | serve it | serve it | compute |
| stale | serve it, queue a refresh | compute; if the service is busy or computing fails, serve it and queue a refresh | compute |
| none | compute | compute | compute |

The response header `X-Pharos-Cache` says what happened (`fresh`, `stale` or
`live`) and `X-Pharos-Cache-Built` says when the body was computed.

**6. Stale entries are refreshed when there is capacity.** One background
refresher recomputes them, two at a time, and starts one only while no
interactive query is running or waiting (its own queries are marked as
background and not counted). Entries stored under another domain version
are rebuilt the same way. Entries a request was just answered with go first, in
the order they were asked for; after those, entries asked for in the last
three days that have gone stale since.

**7. Entries leave by disuse or size.** An entry nobody asks for in 14 days
is dropped, and the least recently used go first once the file holds more
than 256 MB of bodies. An entry stored under another domain version is not
served.

## How the frontend chooses

`web/src/result-cache.ts` is the whole rule:

- **A page loading or revalidating its content asks `cached`.** Whatever is
  stored is drawn at once. A stale answer is shown with its real age (the
  card's "Last updated") and marked as refreshing; the page asks again after
  5, 15, 45 and 120 seconds, which costs one stored-row read each time, until
  the answer is fresh. A hidden tab waits until it is shown.
- **A person pressing a page's Refresh asks `fresh`.** Stored answers are used
  only while current; the rest are computed now, unless the service is busy.
- **A person asking for one thing again asks `live`** (a card's own Refresh).
- **Work that only prepares content nobody is looking at** (prewarming the
  Card Library) asks `cached` and does not follow up.

A stored answer echoes the clock it was computed at, and the frontend checks
it against the request as it was asked then.

## Domains

A domain is one kind of response. Today there is one:

| Domain | What it stores | Stamp | Maximum age for clock-relative requests |
|---|---|---|---|
| `query-v3` | `POST /api/query/{dataset}/query-v3` responses | the dataset's cached-view generation (core, events or all; see `queryDatasetGeneration`), the service's time zone and the running build (an update can change an answer without changing a row) | 10 minutes |

Query requests are made independent of the clock in two ways. `now` is left
out of the key. A filter value that is an instant a whole number of hours
from `now` (a window the client computed from its page load, such as "the
last 37 days") is stored as that distance, and put back for the clock of
whoever computes it. Calendar bounds such as the start of today stay as they
are, so those entries turn over once a day.

Card requests from Home and the Card Library use the cache, including the
cards drawn by a dashboard's own code: the policy travels on the request's
abort signal (`resultScope`), so code that only passes a signal down joins
without knowing about it. Dashboards and tables opened as pages do not yet
name a policy.

## Adding a domain

1. Register it:

   ```go
   var things = registerResultDomain(&resultDomain{
       name: "things", version: "1", maxAge: 10 * time.Minute,
       stamp: func(ctx context.Context, s *Server, scope string) (string, error) { … },
       build: func(ctx context.Context, s *Server, replay []byte, now time.Time) ([]byte, error) { … },
       fail:  func(w http.ResponseWriter, err error) { writeError(w, err, 500) },
   })
   ```

   - `stamp` names the state of the data the response reads. It must change
     whenever the response could, must survive a restart, and should be
     cheap: it is read on every cached request. Return an error when the
     state cannot be named; the request is then answered live.
   - `build` computes the response from `replay` alone. The refresher calls
     it with no HTTP request, so everything the response depends on (URL
     parameters, settings) has to be in `replay`.
   - Bump `version` when the response's shape or meaning changes.

2. In the handler, turn the request into a `resultRequest` (`replay`, the
   `scope` passed to `stamp`, and whether it reads the `clock`) and call
   `s.serveResult(w, r, things, request)`.

3. In the frontend, send `resultPolicyHeaders(policy)` with the request and
   read `resultServed(response.headers)`; use `followStale` if the view
   should catch up by itself.

Do not name a policy on a read that spans several requests which must agree
with each other, such as paging through rows and checking that the total did
not move. Entries are refreshed one by one, so such a read could mix answers
computed at different times. The writing study on Home is read this way and
stays outside the cache.

Keep per-person or per-session state out of a domain unless it is part of
`replay`: an entry is served to every request with the same key.

## What it does not do

- It does not make a first-ever request faster: nothing is stored yet.
- A development catalog (no library descriptor) is known only by its path:
  replace one with an older copy and its generation can later reach a number
  seen before. Delete the file after doing that. Libraries are not affected,
  because a restore changes their instance.
- The refresher does not make the work cheaper. After a change that touches
  every card, Home's entries take a few minutes to catch up, in the
  background and only while the service is otherwise idle.
