# Handoff: retire what is left of the Python importer

## Assignment

Pharos's importer is Go. Two things still assume a Python one: a superseded
migration script with its test, and docs describing a Python importer. Delete
the script if nothing still needs it, and correct the docs.

## Evidence

- **The script.** `tools/backfill_codex_models.py` and `tools/test_backfill_codex_models.py` are a one-off catalog migration from late September, last updated in #104.
  - `docs/token-accounting.md` now says existing catalogs migrate automatically and retained source captures are reparsed, and the Go ingester handles Codex `turn_context` model and effort.
  - No doc or script references the tool.
- **The docs.**
  - `docs/token-accounting.md` line 32 says "The Go importer, Python importer, and browser run the same language-neutral accounting fixtures". No Python importer exists. The browser half now runs in node: the reader's tests in `web/tests/unit/reader/`, against `testdata/token-usage-fixtures.json`.
  - Lines 34 and 40 also mention Python. Line 34 is a historical validation report, so keep its facts and make its tense clear. Line 40 describes catalogs "written by the Python importer"; if such catalogs can still be opened, keep the sentence.
- **Not in scope.** `tools/test_dev.py` and `tools/test_concurrency_prototype.py` are live and stay.

## Done when

- **The script.** It and its test are deleted after confirming no supported catalog still needs them. One way to check is whether `pharos refine-usage` or ingest covers what the script did. If something still needs it, document when to run it instead.
- **The docs.** `docs/token-accounting.md` describes the current fixture runners (Go and node).
- **Checks.** `python3 -m unittest discover -s tools` still passes.
