# Plugin receiver

Design, 2026-11-02. Nothing here is built. This is the service side of
[plugins](plugins.md): what a cloud receiver such as Pythia's must do so that
the guarantees Pharos makes to the person sharing hold end to end. Pythia's
service is the first implementation; the protocol is public, specified in
[`receiver-api.openapi.json`](../contracts/plugins/receiver-api.openapi.json)
with bodies in [`receiver.schema.json`](../contracts/plugins/receiver.schema.json)
and [`report.schema.json`](../contracts/plugins/report.schema.json).

## Requirements

What the Pharos side assumes of a receiver:

1. **Setup is the only time it initiates anything.** After registration it
   only answers Pharos's requests.
2. **Deliveries are idempotent and atomic per run.** Batches can be retried; a
   run's batches apply together at commit, or not at all.
3. **Retractions are honoured** by deleting the content, and a retracted item
   cannot come back from a late or replayed delivery.
4. **Restatements replace** the periods they name.
5. **It notices its own data loss** and asks for everything.
6. **It cannot ask for more than was consented.** It may only ask Pharos to send
   again what the permissions already allow.
7. **Installations are isolated.** A credential reaches one installation and
   can only deliver and say hello.
8. **The person sharing can see everything the receiver holds about them**, and
   who at their organization looked at it.
9. **The person can disconnect from either side.** Revoking on the receiver
   pauses the plugin in Pharos at its next run.
10. **Schemas are negotiated.** The receiver says which export dataset versions
    it accepts.

## Tenancy

An **organization** has **members**, grouped into **teams**, with roles (admin,
manager, member). A member has one **installation** per Pharos library that
shares with the organization; a person with a work laptop library and a desktop
library has two.

An organization chooses a **plan**: a published plugin manifest, such as "Usage
stats" or "Usage and conversation metadata". Because a manifest's permissions are
static, changing what an organization collects means publishing a new manifest
version, which every member consents to again in Pharos. Members who do not
keep sharing at the previous version.

## Setup

1. An admin invites members. Each invitation links to a receiver page that
   explains, in the same plain language as Pharos's consent screen (rendered
   from the plan's manifest), what will be shared and how often, with an **Open
   in Pharos** button: `pharos://install?manifest=<plan>&invite=<code>`.
2. Pharos shows its consent screen. **No** ends here, and the receiver learns
   nothing beyond that the invitation was not accepted.
3. **Yes** opens `registration.url` in the browser, following OAuth for native
   apps (RFC 8252): an authorization request with PKCE, the invitation code
   (Pharos passes it through without reading it), the installation's sender ID
   and permissions hash, and a loopback redirect. The member signs in, with SSO
   where the organization has it.
4. The receiver creates the installation, bound to the member and to the
   manifest version consented to, and redirects to the loopback with a code.
   Pharos exchanges it at `registration.token_url` for a refresh token,
   short-lived access tokens scoped to that installation, and the
   organization's `salt`, and stores the refresh token and salt in the library.
5. The first run says hello with no receipt, so the receiver answers
   `everything`, and Pharos backfills within the permissions' window.

## Protocol

`pharos-receiver/1` over HTTPS, bearer access token, JSON bodies, gzip allowed.

### Hello

`POST /v1/hello`:

<!-- contract: receiver.schema.json#/$defs/hello_request -->
```json
{
  "sender_id": "snd_3f9cA1",
  "plugin": {
    "id": "software.pythia.usage",
    "version": "1.4.0"
  },
  "permissions_hash": "sha256:a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1",
  "datasets": [
    "usage/1",
    "library/1"
  ],
  "receipt": "rcpt_000148",
  "run_id": "run_20261102T150000Z_77c0"
}
```

Answer:

<!-- contract: receiver.schema.json#/$defs/hello_response -->
```json
{
  "need": "changes",
  "accepts": [
    "usage/1",
    "library/1"
  ],
  "max_batch_bytes": 4194304,
  "retention_days": 395
}
```

`need` is `changes`, `{"everything": {"reason", "from"}}` (with `from`
optional), or `{"resend": {"ids", "periods"}}`. A permissions hash other than the installation's consented one is
refused with `409`, which Pharos shows as a mismatch to resolve by updating the
plugin.

### Receipts

Each committed run gets a receipt, an opaque value that names the
installation's commit sequence number. Pharos sends the last receipt it
received with every hello:

- the latest receipt: the receiver has everything Pharos counts as
  acknowledged, so `changes`;
- an earlier receipt it knows: the last commit's response was lost; Pharos
  never counted that run as acknowledged and sends its changes again, which
  idempotency absorbs, so still `changes`;
- a receipt it does not know: the receiver lost data, for example by restoring
  a backup, so `everything`.

### Batches and commit

```text
POST /v1/batches     Idempotency-Key: <run_id>/<n>     pharos-report/1 batch
POST /v1/commit      {"run_id": …, "batches": N}
```

Batches are staged until commit. Commit applies the run in one transaction and
returns the next receipt:

- **Upserts** apply only when their version (the conversation's change sequence
  number in Pharos) is newer than the stored one and newer than any tombstone.
- **Retractions** delete the content and keep a tombstone with only the opaque
  ID, version and time.
- **Restatements** replace the named periods.

Nothing is removed by absence: a run that sends everything again does not imply
that what it left out is gone. Pharos tracks what each receiver may hold and
always says so explicitly with a retraction.

A batch whose fields fall outside the installation's consented permissions is
refused with `422` naming the field. That should never happen, since Pharos
enforces permissions itself, but it catches plugin bugs and is a second check.
A missing batch at commit is refused with `409`, so Pharos retries the run.

### Ending

`DELETE /v1/registration` (from Pharos, on uninstall) and revocation by the
member or an admin on the receiver both end the installation. The receiver then
answers `401`, which pauses the plugin in Pharos with a notice. What happens to
data already held is the receiver's policy; it should say so on the invitation
page.

## Data model

| Table | One row per | Holds |
| --- | --- | --- |
| `organizations`, `teams`, `members` | tenant, team, person | roles, SSO, plan; each organization's salt for opaque IDs and hashes, issued at registration |
| `installations` | member × Pharos library | sender ID, plugin version, permissions hash and consented permissions, status, last hello, last receipt |
| `credentials` | refresh token | hashed token, installation, issued, rotated, revoked |
| `runs` | run | run ID, installation, need answered, batches, committed at, receipt |
| `staged_batches` | batch before commit | idempotency key, size, SHA-256, payload |
| `items` | installation × opaque ID | kind, version, payload, last run |
| `tombstones` | retracted item | installation, opaque ID, version, retracted at |
| `periods` | installation × dataset × period × group | fingerprint, values, last run |
| `member_activity` | delivery or access | what arrived when, and which admin or manager viewed what; shown to the member |

Opaque IDs are keyed by the organization's salt, so the same conversation has
the same ID whichever library or member sends it. Members' totals count each
conversation once across their installations, and a conversation arriving from
two different members flags accounts that probably belong to one person, or a
library registered twice.

## Privacy obligations

- Retracted content is deleted at commit; tombstones hold no content.
- Each member's transparency page shows every item and period held about them,
  and the access log of who viewed it.
- Retention follows an organization policy shown on the invitation page and
  returned in hello, so Pharos can show it on the plugin's page.
- Organizations see what the plan's permissions allow and nothing more: the
  plan is the contract, and a manager's view cannot widen it. Whether managers
  see individuals or only teams will be part of the plan, and so of what each
  person consents to; it is not part of this first design.
- An installation that has stopped saying hello is shown as **not sharing
  recently**, without guessing why.

## Operations

Per-installation rate limits; batch size from hello; refresh-token rotation;
encryption at rest; payload validation against the export dataset schemas;
dataset versions supported side by side while members upgrade.
