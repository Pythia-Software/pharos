# Pharos Query Table backend patch

This directory retains the MIT-licensed Go backend from Query Table **v0.7.1**.
The frontend stays on v0.7.1. The root `go.mod` replacement makes these backend
fixes reproducible without modifying the module cache or requiring an upstream
release.

Local changes:

- Inline cheap native field, CASE and numeric-guard projections in SQLite.
  Keep datetime/function payload evaluation materialized.
- Reuse executor-validated typed values and their error sidecars after staging.
  Caller-provided storage still passes the original validation.
- Fuse scalar V3 group summaries through the existing expression compiler and
  exact aggregate functions. Sampled summaries retain their original execution
  and budgets. Fall back when a combined plan exceeds compiler limits.
- Materialize representative rows once for both identity validation and update.
- Permit an explicit host group budget up to 100,000. Pharos uses this only for
  Usage's compact facts; other dataset budgets remain unchanged.

Revision checks, source authorization, staging byte/population checks, numeric
errors, lazy formula branches, representative validation, and cancellation stay
on their existing paths. Host regression tests compare fused summaries with
independent metric execution.

Upstream tests are included. Two tests reference schema fixtures outside the
published Go module and cannot run from that module's zip:

```sh
GOWORK=off go test github.com/Pythia-Software/query-table/backends/go \
  -skip 'TestExampleSchemaLoads|TestRelativeDurationFixtures'
```

Review local changes against v0.7.1 when updating this copy; remove the root
replacement once an upstream release includes equivalent fixes.
