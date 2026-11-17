# Inactive Teams usage query foundation

This slice compiles and evaluates a deliberately narrow synthetic `usage_1` query using the released Query Table V3 source planner inside one SQLite transaction. Its registry, model families, price and taxonomy IDs are fixture-only. It does not advertise or enable typed execution.

The evaluator accepts only facts that its caller has already verified for current ownership, assignment, anchor and protection in the same transaction. The production entry point fails closed: a future loader must perform those checks before reading fact JSON, pin the accepted source/profile bindings and revisions, and preserve the reviewed distinction between protection and classifier progress. The evaluator's synthetic witnesses do not themselves establish production authority.

The exact-decimal pricing helper requires the pinned embedded book and an identical current catalog book; edited or unproven books produce unknown cost. A production calculation contract needs separately versioned immutable price-book and taxonomy identities. Source-binding staleness and live protection checks remain the future loader's responsibility. Fixture results are not production claims.
