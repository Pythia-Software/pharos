# Handoff: one catalog of authorship span reasons

## Assignment

The Human Words **Rules** filter offers the reasons the authorship classifier
gives a span, such as "Code block" or "Quoted with >". The classifier and the
filter options are kept in sync by hand, so a new classifier reason can ship
without a filter option. Give the classifier one canonical catalog of
reasons. Make the schema's options and the test both come from it.

## Evidence

- **The options.** The `rules` field's static options are listed by hand in `schemas/writing_messages.schema.json` (`filter.values.options`).
- **The classifier.** Its reasons are produced in `internal/archive/authorship*.go` and rendered into rules by `spanRule` (`authorship.go`).
- **The test.** `TestSpanRulesMatchTheSchema` (`internal/archive/writing_messages_test.go`) checks a hand-kept list of example reasons against the schema options, so it only catches reasons someone remembered to list.
- **A rejected approach.** During #147 one attempt parsed the classifier's Go source to collect every reason. It was reverted because it fails on refactors that keep behaviour.

## Approach

- **The catalog.** Introduce a table of reasons, or reason templates where a reason carries particulars such as counts or durations. The classifier labels spans from that table, and every rule `spanRule` can produce is enumerable from it.
- **Generated options.** Generate the schema's `rules` options from the catalog, or check them against it in Go. Schemas are embedded from `schemas/` (`schemas/embed.go`) and checked by `npm --prefix web run check-schemas`. Pick whichever keeps a single source of truth with the least machinery.
- **The test.** Replace the hand-kept list in `TestSpanRulesMatchTheSchema` with an enumeration of the catalog.

## Done when

- Adding a reason to the classifier without it appearing in the Rules options fails a test.
- Existing stored rules still match their options; don't rename existing reasons.
- `go test ./...` and `npm --prefix web run check` pass.
