# Handoff: move to a supported Node and pin it

## Assignment

Development and the test tooling run on Node 20.19.1. Node 20 reached end of
life on 2026-04-30. The repository doesn't say which Node it needs: no
`engines` in `web/package.json`, and no `.nvmrc` or `.node-version`. Move to
the current Node LTS and pin it. Then simplify the test runner where the
newer Node removes workarounds.

## Evidence and opportunities

- **What gets easier on a newer Node.**
  - **`--test-isolation=none`.** It runs test files in one process instead of one per file; `web/tests/run.mjs` starts a process per file today.
  - **`--test-global-setup`.** The browser tier could launch its one browser here, instead of `run.mjs` starting a browser server and passing its endpoint by environment variable.
  - **Per-test timeouts.** On Node 20, `--test-timeout` effectively bounds a whole file. The browser harness works around that by setting a 30 s limit on each test (`web/tests/browser/harness.mjs`).
- **What still needs bundling.** Type stripping would not replace the esbuild step in `web/tests/build.mjs`, because `web/src` uses extensionless imports, `.tsx`, JSON and CSS imports.
- **Where Node is used.**
  - `macos/build-app.sh` runs `npm ci` and the frontend build when npm is available.
  - `tools/dev.sh`, `tools/dev-ui.sh`, `tools/usage-bench.cjs` and `tools/transcript-bench.cjs` all use Node.

## Done when

- The repository pins the chosen LTS (`engines`, plus `.nvmrc` or `.node-version`), and `AGENTS.md` and `README.md` say which Node to use.
- On the new Node these pass:
  - `npm ci`, `npm --prefix web run check` and `npm --prefix web run test:full`;
  - `npm --prefix web run test:browser` on Chromium and WebKit;
  - `npm --prefix web run test:e2e` (macOS);
  - `macos/build-app.sh`.
- Any runner simplification keeps the tiers' behaviour: the unit tier never starts a browser, and each tier prints its slowest tests. Report the before and after times.
