# Manual report-series consent fixture

`manual_series_config.json` is synthetic. Its exact canonical bytes have SHA-256
`01779e79d6812c11354ba411b435f5f98d7161d8a8c45ca83504f11e8d8cb3be`.
The server and client consume independent copies; the contributor's existing
signed work and reviewed submission bind this entire digest. The descriptor
permits only a named-reader, web-only aggregate of `total_tokens`, with an
as-of completeness indicator and manually requested snapshots. The required
ordered disclosure codes bind the warning that snapshot differences may reveal
individual contributions and that later withdrawal does not retract an already
published snapshot. Retention and explicit invalidation continue to apply.

The v2-only capability is intentionally not advertised in this slice. It can
be emitted only on negotiated manifest v2, never on v1 fallback, after the
matched Teams gate and actual signed review/publication flow are verified.
