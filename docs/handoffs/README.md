# Handoffs

Each file here is a self-contained assignment for an agent. It says what to
change, the evidence for the problem, where to start, and when the work is
done.

The handoffs below came out of the test-suite work in
[`docs/testing-survey.md`](../testing-survey.md) (PRs #143–#157). They assume
that work has landed: start from a `master` that includes #157. Follow
[`AGENTS.md`](../../AGENTS.md) for test commands and the rules for writing
tests.

## Product bugs

| Handoff | Problem |
|---|---|
| [library-find-generation-check.md](library-find-generation-check.md) | Library Find and text search return HTTP 500 when a 100 ms check after each page times out on a busy machine |
| [machine-time-long-window-budget.md](machine-time-long-window-budget.md) | Machine Time over 90 days can exceed its 50,000-group budget and show an error instead of data |
| [usage-filtered-metrics-fast-path.md](usage-filtered-metrics-fast-path.md) | Filtered Usage cost and distinct-count metrics still take the slow path; confirm Machine Time's all-time CPU |
| [query-table-fork-reduction-reuse.md](query-table-fork-reduction-reuse.md) | Our patched Query Table adapter re-scans the source when `AVG` or `MIN` is batched with `SUM` |
| [span-reason-catalog.md](span-reason-catalog.md) | Authorship span reasons and the Human Words Rules filter options are kept in sync by hand |

## Layout and focus bugs

These were found by the structural browser suite. An open one runs as a
`todo` (the `KNOWN` list in `web/tests/browser/structure.test.mjs`, or a `todo`
in `focus.test.mjs`) so the suite stays green.

A fix is done when:
- its `KNOWN` entry or `todo` is removed;
- `npm --prefix web run test:browser` passes on Chromium and with
  `PHAROS_BROWSER=webkit`;
- no new pixel, CSS-value or copy assertions were added.

Before and after screenshots are welcome in the PR; set
`PHAROS_UI_SCREENSHOTS=dir`, and don't assert on them.

| Handoff | Pages and widths |
|---|---|
| [focus-findings-dialog-webkit.md](focus-findings-dialog-webkit.md) | Findings prompt dialog in WebKit, which is also what the app ships in |

## Tests, tooling and runtime

| Handoff | Problem |
|---|---|
| [go-test-parallelism.md](go-test-parallelism.md) | The Go suite runs one test at a time; parallelism is the next large speedup |
| [node-runtime-upgrade.md](node-runtime-upgrade.md) | Node 20 is past end of life, and the repository does not pin a version |
| [macos-test-harnesses.md](macos-test-harnesses.md) | The manual macOS harnesses: one is broken, one may be stale, one has no script, and one wipes `web/node_modules` |
| [retire-python-importer-remnants.md](retire-python-importer-remnants.md) | A superseded Python migration script and docs that still describe a Python importer |

## Upstream Query Table

Requests for the Query Table library itself are collected in
[`docs/query-table-next-release-handoff.md`](../query-table-next-release-handoff.md):

- **QT-14:** fuse metrics over different fields in one pass, so Pharos can
  delete its native count and sum path.
- **QT-15:** let computed-column previews use the negotiated transport, so
  Pharos can retire its legacy row route.
