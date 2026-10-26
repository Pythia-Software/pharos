# UI feedback and the private inbox

Pharos's **Send feedback** header tool opens a report composer. A report can be
general commentary, a bug, a feature request, or a prompt to review. It can also
contain several element annotations collected on different pages. The existing
annotation/copy workflow remains available.

An annotation records its URL when the user selects the element. Including that
URL or the element's text in an upload requires checking the corresponding
option. **Take a screenshot of this element** captures that element, shows a
preview, and stores the image in IndexedDB beside the annotation draft. Images
never go into preference values or localStorage. Credentials and known token
parameters are excluded from captured URLs; other query parameters are visible
in the report preview.

The send dialog previews every included annotation and screenshot. Users may
exclude annotations from this report, provide an optional email, and explicitly
choose to remember that address on this device. **Forget my email** removes the
remembered contact and clears the current field. A draft may temporarily retain
its email while the report is unfinished. Sending succeeds only after the server
confirms the matching report ID. Failed sends preserve the draft; retries of an
unchanged payload reuse its ID, including after reopening the app. Editing a
pending payload creates a new ID.
The reviewed selection and edited diagnostic logs are saved in IndexedDB.
Closing or reopening keeps excluded annotations excluded; adding another
annotation includes the new element without restoring prior exclusions.

Diagnostics are opt-in, editable before sending, and capped at 64 KiB each for
browser errors, failed requests, and service stderr. Request diagnostics capture
method, path, time, and status, never headers or bodies. Known credential formats
and user home directory names are redacted. The Go executable mirrors stderr
into a bounded memory tail; it does not create a log file. Reports and optional
contact details are used for feedback follow-up. Resolution emails are manual.
Submitted prompts are data for review; this service never executes them.

## Boundaries and extraction

- `internal/uifeedback/protocol`: v1 Go wire types and validation, with no cloud,
  auth, or server imports. Pharos imports only this small package.
- `internal/uifeedback`: store interface, GCS
  implementation, Pythia authentication, HTTP endpoints, and Twilio notification
  delivery. It imports no archive/catalog code and uses no database.
- `cmd/feedback`: independent Go executable for the hosted service. It imports
  only the feedback package and Google application-default credential support.
- `web/src/uifeedback/model.ts`: matching TypeScript protocol types and pure
  transformations, independent of Pharos.
- `web/src/uifeedback/inbox.tsx`: the standalone admin Query Table. All filtering,
  sorting, grouping, metrics, and selection run over its complete client rows.
- `web/src/uifeedback/pharos-client.tsx` and `internal/archive/feedback.go`: the
  Pharos-specific adapters. The classic annotation script exposes only
  `window.pharosFeedback`; the client exposes `window.uiFeedback`.
- `deploy/feedback`: deployment, lifecycle policy, container, and Auth proposal.

The Go package, generic TS files, and deployment directory can move into another
repository together. Other applications implement their own adapter to the v1
protocol. Application identity is descriptive user-supplied data, not proof of
the caller's identity.

## Storage and HTTP

Each report is a single immutable JSON GCS object, including base64 raster
screenshots and diagnostic text:

```text
uifeedback/v1/submissions/<UUIDv4>.json
```

Creation uses `ifGenerationMatch=0`. A hash of the validated submission makes
uncertain retries idempotent while refusing an existing ID with different
contents. There is no separate attachment upload or partial report to clean up.
The public receipt contains only the report ID and acceptance flag.

Tags are a JSON string in the object's `metadata.tags`. Notification completion
uses `metadata.notified`. These fields can change without rewriting the report
or resetting its lifecycle age. Admin mutations include the observed
`generation:metageneration` revision; concurrent changes return 409 and require
refreshing. Admin deletion removes the whole GCS object and local cache entry.

Every inbox refresh performs a fresh paginated object listing, so deletion,
expiry, and changes from another Cloud Run instance are observed. Immutable
document bodies are cached by generation in a bounded 32 MiB memory cache.
Listings reduce each document to a lightweight summary as they iterate, so
screenshots and logs do not accumulate in the listing response's working set.
Reports older than 90 days are omitted immediately by the application. The GCS
lifecycle policy removes the stored objects asynchronously after that age.
Soft deletion and object versioning are disabled on this dedicated bucket so an
explicit UI delete does not leave retained versions.

```text
POST   /v1/submissions             anonymous, validated intake
GET    /                          authenticated admin UI
GET    /api/submissions            authenticated summaries for Query Table
GET    /api/submissions/{id}       authenticated complete report
PATCH  /api/submissions/{id}       authenticated tags + revision
DELETE /api/submissions/{id}       authenticated If-Match revision
GET    /auth/login                Pythia hosted login with PKCE
GET    /auth/callback             validate login state and exchange code
POST   /auth/logout               clear the local admin session
POST   /events/storage            Google-authenticated Pub/Sub push only
GET    /health                   readiness
```

Admin requests require a Pythia-signed user JWT with the configured project
audience and email `grady@pythia.software`. Service tokens, PATs, and other users
are refused. Browser sessions use an encrypted HttpOnly Secure SameSite cookie;
the stable encryption key lives in Secret Manager. Sessions follow the 15-minute
access token lifetime; the UI offers sign-in again on expiry. Mutations reject
cross-origin browser requests. Report data is never served publicly. Static
assets and the login entry point contain no report data.

Intake accepts at most 8 MiB of JSON, 20 annotations, 16 KiB of commentary per
annotation, and approximately 1.5 MiB per PNG/JPEG screenshot, up to 4096 pixels
per dimension. Pharos generates images at at most 2048 pixels per dimension.
Creation also checks the encoded stored document against the reader's size
limit, allowing 4 KiB beyond the request limit for server fields. A report whose
JSON escaping exceeds that bound is rejected with HTTP 413 before storage.
The receiver limits intake to 30 requests per minute per instance. The default
deployment has one maximum instance and concurrency eight; this is a modest
anonymous inbox, not a distributed abuse-prevention service.

Pharos uses its authenticated, same-origin `POST /api/feedback/submit` proxy. The
proxy forwards only the reviewed JSON to the fixed hosted endpoint, with no
Pharos cookie, bearer token, or other request headers. Override the hosted
origin with `PHAROS_FEEDBACK_URL` when running a development service. No Google
credentials are distributed with the app.

## Deployment

The initial deployment uses GCP project `pythia-xplo` in `us-central1`:
`https://pythia-feedback-iqj5l54y3q-uc.a.run.app`. Pharos uses this endpoint by
default. Its submissions bucket is `pythia-xplo-ui-feedback`.

Use Node 24 and run `tools/setup.sh` on a fresh checkout. `npm --prefix web run
build` builds both ignored UI bundles; no generated JS or CSS is committed.
Go module downloads require access to the private Pythia Auth repository and:

```sh
export GOPRIVATE='github.com/pythia-software/*,github.com/Pythia-Software/*'
```

Run the deployment script with an explicit project and the same send-only Twilio
Email API credentials/verified sender used by Explo. It references the existing
Twilio secret in Secret Manager; it does not retrieve its value. The cookie key
is generated once into a separate secret.

```sh
GCP_PROJECT=your-project \
TWILIO_SID=your-send-only-api-key-sid \
TWILIO_SECRET_NAME=your-secret-manager-secret \
EMAIL_FROM=your-verified-sender \
deploy/feedback/deploy.sh
```

Defaults are Cloud Run `pythia-feedback` in `us-central1`, bucket
`<project>-ui-feedback`, Auth project `ui-feedback`, and a zero minimum instance
count. The script compiles Linux Go locally and uploads only that binary and the
container recipe to Cloud Build, so private module credentials are not sent to
the builder. A dedicated build account reads the separate
`<project>-feedback-build` staging bucket, writes only to the feedback image
repository, and writes build logs. Staged binaries expire after seven days.
The runtime account has object access only to the submissions bucket
and access to the two configured secrets. There are no account keys.
The Storage client requests the `devstorage.full_control` OAuth scope because
GCS metadata PATCH rejects the read/write scope. Effective permissions remain
limited by the dedicated bucket's object IAM grant.

The script writes a deployment receipt and a Pythia Auth proposal under
`.context/`. For first setup, validate and open the proposal using Pythia Auth's
`pythia-plan` CLI (`~/gbdubs/pythia-auth`, `make plan-cli`). Its human-confirmation
page creates the allowlist project, grants the sole admin, and adds the exact
callback URI. Reusing an existing Auth project needs only the missing operations;
`project.create` is deliberately not idempotent. All admin routes fail closed
until the project is configured.

Runtime environment:

| Variable | Purpose |
|---|---|
| `FEEDBACK_BUCKET` | Dedicated private bucket |
| `FEEDBACK_ORIGIN` | Canonical HTTPS Cloud Run origin |
| `FEEDBACK_AUTH_PROJECT` | Pythia Auth project/audience |
| `FEEDBACK_COOKIE_KEY` | Base64 32-byte secret |
| `FEEDBACK_PUSH_ACCOUNT` | Dedicated Pub/Sub push service account |
| `TWILIO_API_KEY_SID` | Send-only Twilio API key SID |
| `TWILIO_API_KEY_SECRET` | Secret Manager-backed Twilio key secret |
| `FEEDBACK_EMAIL_FROM` | Verified Twilio sender |
| `PORT` | Cloud Run port, default 8080 |

## Notifications and operation

GCS `OBJECT_FINALIZE` events for the report prefix publish to
`ui-feedback-created`. `ui-feedback-email` pushes to the same Cloud Run service
using its dedicated Google service account and exact service audience. The
handler validates Google's signature, issuer, audience, and expected account.
Successful Twilio acceptance writes the completion receipt into object metadata.
Tag edits never produce finalize events or additional notifications.

Email goes only to `grady@pythia.software` and contains a private report link.
Submission text, email addresses, screenshots, and logs are not copied into the
notification or application logs. Sending/receipt storage is at least once: a
crash after provider acceptance but before receipt storage can duplicate a
notification. Failed deliveries retry with backoff; after ten attempts they go
to `ui-feedback-failed`, retained by `ui-feedback-failed-review` for inspection.
Both subscriptions retain undelivered messages for seven days. There is no timer,
resident worker, or database preventing scale to zero.
The subscriptions have no idle expiration, so a quiet inbox does not stop future
notifications.

Useful checks:

```sh
go test -race ./internal/uifeedback
go test -vet=off -run '^TestFeedback' ./internal/archive
go test -short ./...
npm --prefix web run check
npm --prefix web run test:browser -- --test-name-pattern='feedback|annotation|annotated element'
```
