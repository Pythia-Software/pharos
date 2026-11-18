# Pharos Teams integration

The integration branch adds a Teams connection screen under Settings. It displays
which library and retained history would be enrolled and opens the configured
Teams service in the system browser. Compare the connection code shown by
Pharos with the code requested by the browser before confirming. Enrollment
credentials are installation-specific, stored outside libraries and backups in
an owner-only application-support directory, and bound to the library instance
and service origin. Restored or moved libraries require reconciliation.

For development, set `PHAROS_TEAMS_URL` to the HTTPS origin of the Teams service
before starting Pharos. An unset value disables connection. This is an origin,
not a URL containing a path, query, fragment, or credentials. Loopback HTTP is
available only to isolated Go tests, never to the application enrollment UI.

This integration remains **paused**: connecting alone does not start polling,
querying, or submitting work. The local gated sender and receipt reconciliation
are internal primitives exercised with a synthetic peer, not a running worker.
Before enabling one, verify those primitives against the service and complete
provider-specific source identity, recovery, a declared integration capability
version, and the remaining policy and consent workflow. Classifier freshness,
shared privacy reconciliation, and local model and tool controls are required
before content work. The current audited numeric query bridge rejects unsupported
shapes and ambiguous sources rather than treating them as zero. See the Teams
service's current API contract for coordinated wire behavior.

The present source adapter is synthetic-only. Production needs provider-specific
stable account identity and a comparable evidence revision derived from canonical
source content, never a timestamp or local path alone. Identical copies currently
have incomparable evidence and can block a complete result; a later slice must
also report genuinely incomplete populations as partial while retaining the
established owner. The numeric bridge currently supports only audited sums of
four token fields; broader queries, classifier freshness, model execution, and
read-only tools remain separate work.

When a worker is enabled, idle polling must use a jittered 10–15 minute
interval so the service can scale to zero. A locally known active request,
foreground action, or explicit Check now may wake it sooner. A request created
remotely while the Mac is idle waits until the next poll unless a separately
designed push channel is available; the UI must show that latency honestly.

Manual report series is advertised by builds with an actual release version of at least `0.10.0`; the service must have the matching route and v3 capability gate before enabling a pilot. Eligible builds declare both `reader_labels_v1` and `report_deterministic_v2` in the v2 manifest alongside the deterministic and source snapshot capabilities. A v1 fallback does not declare either v2-only feature. The directory read supplies presentation labels only; signed audience IDs and the exact config and payload hashes remain the consent bindings.
