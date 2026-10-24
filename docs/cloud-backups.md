# Cloud backup setup and recovery

Open **Settings → Backups** (`/settings/backups`). The local drive-backup
controls remain available. Cloud planning is hidden behind **Explore cloud
backup options** with a **Beta** chip, so no estimated price is visible on first arrival. Connected destinations show a price-free status panel below that button, including the next run, latest results, schedule controls and Back up now. Opening it
reveals a single compact card, with Calculator, Configure, Commands, Test,
First backup and Test restore as a six-step wizard across its header. Settings, totals and the smaller monthly
cost chart share that card. Controls and results use flexible columns, and the
graph and summary fill the entire results column as the card grows or shrinks.
Closing it returns focus to the Explore button.
Saved configurations remain in Pharos's database.

## Setup flow

1. **Calculator:** shows provider, backup frequency, retention method and the
   corresponding backup count or age as four primary controls. It starts with
   a complete default plan and exact dollar estimates. The results pane shows
   totals for the next full calendar month (named beside the amount) and the
   first 365 days, plus a monthly stacked bar chart. Each
   calendar month pairs a solid no-growth bar with a dotted-outline growth bar.
   Colors split storage from upload request fees; colder classes also show
   early-deletion fees separately. Hover, tap or arrow keys reveal both
   scenarios' exact breakdown, including upload fees below one cent. Small
   segments retain their true scale. Partial months show their forecast-day
   count in the readout. The chart keeps readable calendar-month labels and
   keyboard focus while exploring.
   Each hovered or tapped bar also opens the standard chart-style popover,
   showing its month, growth scenario, storage, upload requests, any early-deletion
   fee and total as separate numeric rows. Keyboard focus opens the
   same popover; left/right change month, up/down change growth scenario and
   Escape dismisses it. The popover stays within the viewport and outside the
   card's clipped bounds.
   A note identifies the estimates as charges from the user's cloud provider.
   **Detailed options** is collapsed initially and contains exact region,
   storage class, pruning grace period, library size, compression and linear
   growth. Upload chunk size is chosen internally. The growing-library
   explanation and pricing sources are also inside this disclosure.
   A visible warning appears throughout setup when frequency and retention
   would prune revisions before the selected class's minimum storage duration.
   It shows the expected age at pruning, unused days billed and first-year
   early-deletion fees, which are already included in the totals. Keeping
   revisions long enough removes the warning, including when the pruning grace
   period provides the extra time.
2. **Configure:** choose the destination label and resource names, and an
   existing AWS CLI profile, GCP project or Azure subscription. GCP also offers
   **Create a new project**, with an editable suggested project ID, an active
   billing account ID and an optional organization or folder under advanced
   settings. The commands create the project, link billing and enable Storage
   and IAM APIs before creating the bucket. Availability, permissions and quota
   are checked by Google when the script runs; failure stops setup. Location and
   class are linked to the calculator: editing either here or there immediately
   updates the forecast and eventual commands. Every available storage class
   shows its first-month and first-year charges at the current calculator
   settings, including growth and minimum billing. Schedule, upload identity
   and SAS lifetime remain in a collapsed advanced section. Field help explains
   the Pharos destination label, archive path and restricted cloud login.
   Uploads use the available connection speed; no bandwidth cap is configured.
   **Require a password to restore** is optional and off by default. This
   setting records only the choice; passwords are never entered in the UI.
   Azure
   supports age-based retention only.
3. **Commands:** review the exact settings and copy the prepared Bash,
   setup-agent prompt or configuration JSON. Save the configuration in Pharos
   to edit or reuse it and add further destinations. Password-protected setup
   has three separately copyable actions: a noninteractive preparation block,
   one standalone terminal command for password setup, then a noninteractive
   cloud-provisioning block. There is no combined copy-all command for this
   mode, including when revisiting a saved destination.
4. **Test:** connect the private setup folder. Pharos reads credential files
   server-side and uploads a 32-byte random probe to the dedicated test object.
   It checks object metadata/read and bucket-list requests return Forbidden
   before saving the credential in SQLite. A failed check saves nothing. Restrictions are checked again before each upload; a credential found granting read/list access is forgotten. The
   provider must return a real object version; a nonversioned destination is
   rejected. Then upload the real 1 MiB test archive, download its exact version
   through the user's cloud login, and verify it. Password-protected downloads
   are verified with a standalone Terminal command and a fresh password entry.
5. **First backup:** once a downloaded test has been verified, start a full
   backup immediately. The server snapshots SQLite through the existing online
   backup routine and copies the library sources into private staging. This
   temporary snapshot does not count as a durable local backup. It creates one
   zstd archive, optionally encrypts it with age, uploads it, and records the
   exact version ID, archive SHA-256, sizes and file manifest. No full library
   is uploaded automatically when the page opens or when a configuration is
   saved. Archive output checks free space before every write, including
   encryption headers and final encoder flushes, and stops before consuming
   Pharos's free-space reserve. Temporary staging is removed after completion
   or cancellation.
6. **Test restore:** copy the version-pinned provider download command and
   choose the local download folder. Unprotected archives can be validated by
   the service; protected ones produce a separately copyable Terminal command:
   `pharos --config PATH cloud-backup verify RUN_ID DOWNLOADED_ARCHIVE`.
   Passwords are read from `/dev/tty`, never stdin or the settings form. The
   command records verification status in SQLite, and the page polls it. A
   completed upload is not labeled verified until the actual file and content
   checks pass. Azure download commands select the configured subscription so
   the CLI obtains credentials for the correct tenant. New recovered copies
   remain isolated from the live library.

The wizard shows real operation state and failure messages. It supports
cancelling service-owned operations; cancel a Terminal verifier with Ctrl-C.
Interrupted processes become failed operations rather than verified restores.
A successful full restore validates archive SHA-256 against the upload ledger,
authenticated age decryption (if enabled), safe paths, every file's size and
SHA-256 against the snapshot manifest, required configuration/catalog files,
and SQLite `integrity_check`. Downloads and unpacking can require substantial
local disk space; the wizard shows archive and staging estimates.

After a successful full restore test, **Enable scheduled backups** activates the
selected cadence. Schedules persist in SQLite and run in the Pharos service;
no launchd job or additional password is required. Daily and weekly times follow
the selected IANA time zone. Every 30 days uses calendar days anchored to the
date the schedule is enabled or changed. A missing daylight-saving time runs
at the next available minute; a repeated time runs once.

Pharos and the library drive must be available. Sleep or downtime coalesces
missed occurrences into one catch-up backup. Only one cloud operation runs per
service at a time, and SQLite claims prevent duplicate full backups to a
destination. Scheduled failures retry after 1 hour, 6 hours and 24 hours, then
return to the regular schedule. Explicit cancellation waits until the next
regular slot. Interrupted uploads are reconciled even with Settings closed;
retries create a fresh archive and upload rather than resuming old network parts.

A schedule belongs to the Mac and library path where it was enabled. Moving or
restoring a copy does not automatically run that copy's schedules; explicitly
enable them at the new location. **Pause schedule** stops future admissions and
lets an upload already running finish; **Cancel backup** stops it. Disconnecting
also disables the schedule. **Back up now** can be used repeatedly and works
while a schedule is paused. Schedule settings can change frequency, time, day
and time zone without reprovisioning the bucket. Such edits do not change the
provider's existing retention rules. The version selector permits validation
of earlier completed uploads even after a newer attempt fails.
No live provider account was provisioned during implementation. Local checks
do not establish successful integration with a live provider account.

Versioned download command references:
[gcloud storage cp](https://docs.cloud.google.com/sdk/gcloud/reference/storage/cp),
[S3 get-object](https://docs.aws.amazon.com/cli/latest/reference/s3api/get-object.html),
[Azure blob download](https://learn.microsoft.com/en-us/cli/azure/storage/blob#az-storage-blob-download).

The starting plan is weekly full backups, four retained revisions, GCP Standard
in Iowa (`us-central1`). It favors immediate recovery and flexible billing.
Switching provider defaults to S3 Standard in Northern Virginia or Azure Hot in
East US; Azure uses 30-day age retention. Location is an explicit example
choice, not inferred from the user's whereabouts. Users can change it before
creating any resources. New plans use the compression benchmark and automatic
linear growth estimate; a manual growth override, including zero, is preserved.

There is no capacity reservation or upfront spending commitment in this flow.
For colder tiers, the minimum storage duration is an automatic consequence of
uploading with that storage class; no separate CLI purchase or commitment is
needed. Deleting early still incurs the minimum-duration charge. The calculator
explains this next to the storage-class option and includes the charge in the
forecast. See [S3 storage classes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/storage-class-intro.html)
and [S3 pay-as-you-go billing](https://docs.aws.amazon.com/AmazonS3/latest/userguide/cost-optimization.html).

For **GCS**, this is a minimum **duration**, not a minimum object size or
storage-capacity commitment. Google explicitly states there is no minimum
object size for these classes. Nearline has a 30-day minimum duration,
Coldline 90 days and Archive 365 days. Deleting a revision before that age
bills the remaining duration as an early-deletion fee. With versioning,
becoming noncurrent does not trigger that fee; deleting the noncurrent
revision does. The forecast tracks each revision from its creation until
pruning, and labels the extra charge **Early-deletion fees** in bars/popovers.
See [GCS storage classes](https://docs.cloud.google.com/storage/docs/storage-classes#available_storage_classes)
and [GCS minimum-duration billing](https://cloud.google.com/storage/pricing#early-delete).

Bucket/account names are prefilled with a suffix **once, visibly in the form**.
They remain editable. The generated commands never add another suffix or
substitute a location. Syntax validation and required account/project fields
run before advancing. Global name availability is verified by the provider
when the user executes the commands, not by the local calculator.

New GCP projects require an existing active billing account and an administrator
allowed to create projects and link that account. Account IDs can be found with
`gcloud billing accounts list`; personal projects can omit a parent. The flow
uses Google's documented [project creation](https://docs.cloud.google.com/sdk/gcloud/reference/projects/create),
[billing link](https://docs.cloud.google.com/sdk/gcloud/reference/billing/projects/link)
and [service enablement](https://docs.cloud.google.com/sdk/gcloud/reference/services/enable)
commands. Existing-project setup does not change its billing account.

The copyable command block runs in a separate Bash process, including when
pasted into a macOS zsh terminal. It creates a new, private destination,
provider-managed encryption, versioning, lifecycle rules and a restricted
upload credential. It writes a private `destination-draft.json` and
`upload-credential.json` under:

```
~/Library/Application Support/Pharos/cloud-setup/PROVIDER/BUCKET_OR_ACCOUNT/
```

The files preserve the exact settings and are read locally by the credential importer. The script refuses to overwrite files from an earlier
attempt. After a partial provisioning failure, repair the specific failed
step instead of repeatedly creating resources.

**Implemented:** planner, provisioning templates, nonsecret saved plans,
SQLite credential import, restricted-access checks, S3 multipart/GCS resumable/
Azure block uploads, optional age archive encryption, upload history, immediate
first backup, ongoing scheduling and repeat manual uploads, downloaded-file verification and standalone disaster recovery.

**Not implemented:** durable resumable journals across service restarts or
administrator-side auditing of native lifecycle rules.
A lost completion response is not treated as successful; repeated explicit
uploads can create additional revisions. Provider cleanup handles abandoned
GCS/Azure upload sessions; failed S3 multipart uploads are aborted where possible.
Credentials never enter browser preferences, ordinary API responses or logs.

## Selected backup and credential design

Each successful backup is an **independent full compressed archive** at one
stable object name, normally `pharos/library.tar.zst`. Replacing it creates a
new cloud object revision. Multipart/resumable requests permit retrying chunks
while the cloud stores a single finished object. Failed uploads do not count
as completed revisions.

Store the restricted upload credential value **directly in Pharos's SQLite
database**, associated with its destination. It travels with the library and
normal database snapshots. This accepted exposure permits upload and, on GCP,
deletion, but must not grant read/list access. Do not use a machine Keychain
reference, separate unlock secret or password prompt for ordinary uploads.
Saved plans use `ui_preferences`; connected destinations use the separate
`cloud_destinations` table. Credentials and encrypted recovery bundles are read
from private setup files into that table. `cloud_backup_runs` stores status and
snapshot manifests. Public responses whitelist nonsecret configuration fields
and never serialize the credential. Removing a connected destination forgets
its upload credential while retaining recovery metadata and cloud objects.

By default use HTTPS and **provider-managed encryption at rest**, without an
archive password or separate recovery encryption key. To recover after
losing a Mac, sign into the cloud account with ordinary read authorization,
choose a completed revision, download and restore it. Cloud account access is
the recovery dependency. The stored upload credential cannot authorize that
download.

Sources: [AWS SSE-S3](https://docs.aws.amazon.com/AmazonS3/latest/userguide/UsingServerSideEncryption.html),
[GCP default encryption](https://docs.cloud.google.com/storage/docs/encryption/default-keys).
Avoid customer-managed or customer-supplied encryption keys in the default
setup, because they add another recovery dependency.

### Optional recovery password

Configure can opt into **Require a password to restore**. Password setup is
performed only in Terminal using the official `age` / `age-keygen` tools
(`brew install age` on macOS). The preparation block checks for those tools
and writes a private Python helper and destination draft. It creates no cloud
resources. Its standalone invocation is copied separately from the before and
after blocks, avoiding an interactive prompt in a shell here-document.

The helper requires a controlling terminal and lets age read the password
there, independently of stdin. It generates a random key pair, pipes the
private identity to age's passphrase encryption, and asks the user to enter
and confirm their password. A third prompt decrypts the encrypted identity
in memory and checks it matches before saving. Plaintext private identities
only cross process pipes; passwords stay inside the age processes. Neither
is written to a file, preferences, command arguments or environment variables.
Memory erasure is best effort; Python and Go do not guarantee removal of all
copies from memory. A failed confirmation or verification leaves no recovery
bundle. Existing recovery material is never regenerated by rerunning setup.

Only a public recipient and a password-encrypted identity are saved in
`recovery-bundle.json` (0600), and the local draft is marked `verified` with
its public recipient. The final cloud block checks these match its exact
configuration and refuses missing, incomplete or stale setup before any cloud
command. It also refuses to overwrite an existing upload credential.

The protected contract is `full_archive_zstd_age_bundle_v1`. The archive starts
with `PHAROS-CLOUD-1\n`, a big-endian uint32 JSON-header length and the encrypted
recovery bundle, followed by age-encrypted zstd/tar payload. The uploader uses
a fresh age file key for each archive. The encrypted recovery identity remains
outside the payload in every backup. Unprotected archives are ordinary zstd/tar.
The tar payload ends with `pharos-cloud-manifest.json`, containing the snapshot
file paths, sizes and SHA-256 values. Private keys are recovered only in memory.

On a replacement Mac, the original Pharos database/configuration is optional:

```sh
pharos cloud-restore '/path/to/downloaded/library.pharos' '/path/to/new-recovery-folder'
```

This standalone command uses the embedded bundle and manifest, prompts in the
controlling Terminal when encrypted, and validates the recovered library in a
new private folder. It never opens or replaces an active library. For the
wizard's Test Restore, the same extractor additionally checks the independent
upload ledger. The extractor rejects links, duplicate paths, unsafe paths,
files following the manifest, oversized manifests, and unexpected contents.
It supports up to 200,000 files and files up to 5 TiB. File contents are staged
with private permissions and failed extraction directories are removed.

Recovery therefore requires the downloaded archive and its original recovery
password, without a separately preserved key file. Ordinary uploads do not
need the password. Forgotten passwords cannot be reset. Store the password
in a password manager; someone holding a recovery bundle can attempt offline
guesses. The public key must also be protected against unauthorized changes
to prevent redirecting future backups to another recipient.

See [age password-protected identities](https://github.com/FiloSottile/age#passphrase-protected-key-files).

## Provider commands and permissions

| Provider | Upload identity | Version retention |
| --- | --- | --- |
| AWS | New IAM user with `s3:PutObject` and `s3:AbortMultipartUpload` on the exact archive key and its dedicated `.roundtrip-test` key; explicit denial of `s3:Get*`, `s3:List*`, `s3:Delete*` | Enables bucket versioning. Count N means N−1 noncurrent versions plus the current version, combined with the minimum superseded age. Count 1 uses age-based expiration of all superseded versions. No expiration of the current revision. |
| GCP | New service account with a dedicated custom role containing only `storage.objects.create` and `storage.objects.delete`, bound to the new bucket | Enables versioning, disables soft delete, and expires only noncurrent generations with at least N newer generations and the chosen minimum noncurrent age. No current-version expiry. |
| Azure | New private Standard LRS account/container, with a revocable HTTPS service SAS granting only `cw` | Enables blob versioning. Previous versions expire by creation age; the current blob remains available. SAS lifetime is configurable. No fabricated native count-retention rule. |

Azure setup also grants **Storage Blob Data Reader** to the administrator's
own CLI identity on the new container, so `--auth-mode login` can perform the
recovery downloads. The Pharos SAS remains limited to `cw`. The identity is
read from the signed-in Azure CLI's access-token claims locally; its access
token is never saved or sent to Pharos. Role assignments can take several
minutes to propagate. See [Azure data-access role assignment](https://learn.microsoft.com/en-us/azure/storage/blobs/assign-azure-role-data-access).

GCP create/delete does not grant `storage.objects.get` or
`storage.objects.list`. Use a fresh service account without other grants:
IAM grants are additive. See [GCP permissions](https://docs.cloud.google.com/storage/docs/access-control/iam-permissions).

AWS's multipart implementation must keep upload IDs and ETags locally rather
than list parts or HEAD the current object. The selected region must be passed
explicitly. Initial [S3 versioning propagation](https://docs.aws.amazon.com/AmazonS3/latest/userguide/manage-versioning-examples.html)
can take 15 minutes. If connecting returns no version ID, wait and retry. The commands handle
`us-east-1` without a LocationConstraint and supply it elsewhere.

The selected storage class is written into `destination-draft.json`; the
uploader must set it for every new revision. GCP also sets the bucket's default
class. AWS/Azure provisioning cannot itself apply a class to archives that
have not yet been uploaded. The schedule in the draft remains disabled until a scheduler is implemented.

Native lifecycle deletion is asynchronous. A retained count is a target,
not an instantaneous storage cap; minimum age and an upload in progress can
increase storage. AWS's version count and age conditions both apply. Azure's
creation-age rule is different from the noncurrent-age rule on AWS/GCP.
Sources: [AWS lifecycle](https://docs.aws.amazon.com/AmazonS3/latest/userguide/intro-lifecycle-rules.html),
[GCP lifecycle](https://docs.cloud.google.com/storage/docs/lifecycle),
[Azure previous-version expiry](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-policy-delete).
Azure Archive cannot overwrite an existing archived current blob; see
[Put Blob restrictions](https://learn.microsoft.com/en-us/rest/api/storageservices/put-blob).
It remains visible as a cost comparison but is disabled as a setup choice.

## Pricing snapshot and calculator

Reviewed **2026-10-22**, USD regional list prices. The UI offers five locations
per provider, each with four storage classes for comparison. Azure Archive is
comparison-only: archived current blobs cannot be overwritten, so this
single-file setup supports Azure Hot, Cool and Cold. Capacity, write-request and
retrieval prices follow the selected location:

| Provider | Priced locations |
| --- | --- |
| AWS | Northern Virginia, Oregon, Ireland, Frankfurt, Sydney |
| GCP | Iowa, Oregon, Belgium, London, Sydney |
| Azure | East US, West US 2, West Europe, UK South, Australia East |

Sources are the official [S3 price catalog](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonS3/current/index.json),
[Deep Archive catalog](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonS3GlacierDeepArchive/current/index.json),
[GCP storage pricing](https://cloud.google.com/storage/pricing), and
[Azure Retail Prices API](https://learn.microsoft.com/en-us/rest/api/cost-management/retail-prices/azure-retail-prices).
AWS Standard rates select the Standard volume type, rather than Files,
Annotations, Vectors or Tables. Multipart charges select the actual API
operation, since initiation/parts can differ from completion. Azure uses
General Block Blob v2, consumption, first-tier LRS meters. GCP hourly capacity
is normalized to 730 hours/month; its regional request and retrieval rates
are common to the offered regional locations.

Pricing data is embedded; no pricing API calls occur
while configuring a destination. Price assumptions are dated and must be
reviewed before release. The restore-egress rate is a user-editable assumption,
not a provider quote. Rates exclude tax, discounts and free allowances.

### Linear growth and first-year forecasting

The service retains one measured logical library size per UTC day in
`library_size_history`, populated when the existing drive-health measurement
refreshes. Complete local-backup records can seed prior observations; partial
backups and future timestamps are ignored. Sizes cover the catalog, captures
and preserved data, rather than process memory or token counts.

With at least 90 days of size history, the daily growth rate is the difference
between today's size and the size 90 days earlier, divided by 90. When the
boundary falls between observations, the boundary size is interpolated.
Shrinking libraries use zero growth for a conservative forecast. Older points
are pruned while retaining the anchor needed to interpolate the boundary.
`/api/health/drive` exposes `cloud_growth` with `bytes_per_day`, `history_days`
and `basis`.

With less history, the fallback is **5% of today's size per 30 days, converted
once to a fixed GB/month amount**. It never compounds. The UI shows both that
amount and why the fallback is in use. The Euclid library has one recorded
full backup, so its actual 90-day byte growth cannot yet be established. It
would be misleading to infer library growth from tokens or process I/O.

The calculator simulates the next 365 days starting with an empty destination:

- Upload one complete compressed snapshot today and then on the chosen cadence.
- Each snapshot uses its upload-day library size plus the fixed linear growth.
  Existing versions retain their original size.
- Apply count/age retention to superseded versions; never expire the current
  revision. Include the minimum superseded age on AWS/GCP.
- Charge daily capacity, actual upload-request counts and minimum-duration
  early-deletion fees when a version expires. Include Deep Archive metadata.
- Sum the next full calendar month, all 365 days, and the last 30 days of the
  year. The first-year figure is an accumulated forecast, not monthly cost
  times twelve.

The price graph groups the 365-day forecast into actual UTC calendar months,
using labels such as Oct 2026 and Apr 2027. Starting mid-month produces partial
first and final months (usually 13 plotted points); each point sums only the
forecast days in that calendar month. Graph months sum to the first-year total.
The first-month headline uses the next full calendar month: an October setup
shows November, including growth and revisions retained since setup. This is
also the rule when setup starts on the first day of a month. Storage-class
comparisons use that same period. The solid baseline fixes library growth at zero while
keeping all other settings identical, including startup, compression, retention,
region and storage class. Slight monthly variation without growth can reflect
calendar-month length, partial months and upload/pruning timing.

Lifecycle lag, transient upload storage, retries, taxes, discounts, free quotas,
provider rounding, restore and compute costs are excluded. A 30-day month is
used for capacity arithmetic; billing calendars and cleanup timing can differ.
This remains a planning estimate. Minimum-duration obligations on revisions
still retained after day 365 can affect later bills. Supported archives are at
most 5 TiB; future library growth can eventually exceed this supported limit.

### Full-library compression measurement

Measured **2026-10-23**, from a separate copy on Euclid:

| Measurement | Result |
| --- | ---: |
| Uncompressed backup files | 142,564,121,707 bytes (142.6 GB) |
| Full `tar.zst` archive | 41,112,195,273 bytes (41.1 GB) |
| Compressed size | **28.8377% of original** |
| Space reduction | **71.1623%** |
| Files | 42,879 |
| Snapshot, compression and validation | 337.3 seconds |

The experiment used APFS clones for captures, preserved data and hosts while
holding shared capture locks, plus SQLite's online backup API with a pinned
read-only source transaction. It included the library configuration and
excluded the app and staging, matching the backup payload rather than all
files on the volume. The isolated files were streamed through `tar` and
`zstd -3 -T2`, followed by `zstd -t` validation. Production files were not
modified. The cloned copy and compressed archive remain under
`/Volumes/euclid/pharos-compression-20261023-090409/`; the experiment and result
receipt are in the workspace's gitignored `.context/cloud-compression/`.

Under Detailed options, the UI defaults to a fixed **30% of original size**,
rounded from the Euclid sample. It is not a new compression measurement of
each user's library. Users can override it under Detailed options. The growth
field's help identifies the number of usage-history days used for a measured
rate; with less than 90 days it explicitly explains the fixed 5% fallback.
Provider-managed encryption does not prevent compression before upload.

**Upload chunk size is automatic**, not a user setting. It controls network
requests within the single large archive. The provider retains one completed
object revision. Smaller chunks reduce the transfer repeated after a failed
request; larger chunks can reduce AWS/Azure request fees. GCP bills one write
for a completed resumable object upload regardless of chunk size. The planner
uses 64 MiB internally and increases it for provider part-count limits when
necessary; legacy per-destination overrides are ignored.

For this measured 142.6 GB library, the measured compression ratio, fixed
+7.13 GB/month fallback growth, weekly backups and four retained revisions,
selected default US locations gave these illustrative forecasts using the exact
28.8377% sample ratio (the calculator now defaults to 30%):

| Class | Next 30 days | First year | Month 12 |
| --- | ---: | ---: | ---: |
| S3 Standard | $2.34 | $53.90 | $5.52 |
| S3 Deep Archive | $0.31 | $14.57 | $1.74 |
| GCP Standard | $2.02 | $46.68 | $4.78 |
| GCP Archive | $0.64 | $35.18 | $4.25 |

A cold tier can have a low starting bill but expensive revision churn. The
calculator shows the first-year effect for the chosen location before asking
for any destination names or account details.

## Remaining work

1. Run the new safety tests and dedicated live-account checks for each provider,
   including read/list denial, overwrite/version receipts, cancellation, provider
   checksum handling and native lifecycle retention. No live provider was used
   during this implementation.
2. Add automatic schedules, notifications and durable multipart/resumable
   journals for restart recovery. Retry/cancellation must preserve uncertain
   completion outcomes and never mark an unconfirmed revision as verified.
3. Add administrator-side lifecycle auditing and a richer history/cleanup UI.
   Current cleanup follows the commands' provider lifecycle policy; forgetting
   a credential in Pharos does not delete provider resources or revisions.
