# Capture push

`pharos capture-push` captures a machine's Claude Code and Codex transcripts
and uploads them to a bucket, where a Pharos library can collect them. It is
meant for machines that do not run Pharos: cloud sandboxes, dev containers,
CI runners and remote development hosts.

It needs no library, catalog or service. It reads one configuration file,
keeps its own state folder, and sends plain HTTPS requests; no cloud
vendor's CLI is involved.

```text
pharos capture-push [--config FILE] [--state DIR] [--wait | --watch | --check | --status]
```

| Form | What it does |
|---|---|
| `capture-push` | Asks for a push and returns at once; a detached worker does the work. This is what an agent hook runs after every turn. It always exits 0 |
| `capture-push --watch` | Stays running. Pushes when it starts, then whenever a transcript changes. For platforms that supervise a long-running process |
| `capture-push --wait` | Pushes in the foreground and prints the pass as JSON. Exits non-zero if the pass failed. For the last step of a job |
| `capture-push --check` | Writes this machine's `hello` to the bucket and prints what it resolved. Proves the configuration before any conversation exists |
| `capture-push --status` | Prints the last pass, the sources found, and whether pushes are paused |

A library collects what was pushed with
[`pharos bucket-import`](#collecting-into-a-library).

On Linux it is one file with no dependencies. Every Pharos release carries
it for both architectures, under names that match `uname -m`, each with a
`.sha256` beside it:

```sh
curl -fsSL -o pharos "https://github.com/Pythia-Software/pharos/releases/latest/download/pharos-linux-$(uname -m)"
chmod +x pharos
```

Replace `latest/download` with `download/vX.Y.Z` to stay on one version. On
a Mac the same command is inside the app:
`/Applications/Pharos.app/Contents/MacOS/pharos`. Only `capture-push` is
supported on Linux; the rest of Pharos is a Mac program.

Code: `internal/archive/capture_push.go` (passes, state, modes),
`capture_push_store.go` (configuration and destinations) and
`capture_push_chunks.go`. Tests: `capture_push_test.go`. Contract:
[`contracts/plugins/push.schema.json`](../contracts/plugins/push.schema.json).

## Configuration

The configuration is `~/.pharos/pharos-push.json`, or the file named by
`--config` or `PHAROS_PUSH_CONFIG`. It follows `pharos-push-config/1`:

```json
{
  "contract": "pharos-push-config/1",
  "access": "write",
  "prefix": "pharos/{owner}",
  "retention_days": 90,
  "destination": {
    "type": "s3",
    "bucket": "example-pharos-transcripts",
    "region": "us-east-1",
    "access_key_id": "AKIA…",
    "secret_access_key": {"env": "PHAROS_PUSH_SECRET"}
  }
}
```

Unknown fields are refused. A secret is written inline, or as
`{"env": NAME}` or `{"file": PATH}` so that it can come from a secret store;
a relative file is read from the configuration's folder.

| Destination | Stored credential | Keyless |
|---|---|---|
| `s3` (and S3-compatible stores, with `endpoint`) | `access_key_id`, `secret_access_key` | `role_arn` and `identity_token` |
| `gcs` | `service_account_key`, an HMAC key as `access_key_id` and `secret_access_key`, or `access_token` (where a token comes from, such as the command `gcloud auth print-access-token`) | `workload_identity_provider`, `identity_token`, and optionally `service_account` to act as |
| `azure` | `sas_url` for the container | `account`, `container`, `tenant_id`, `client_id` and `identity_token` |
| `http` | `url_template` containing `{key}`, and optionally one `header` | |

**Keyless** means nothing secret is stored on the machine. `identity_token`
says where a signed identity token (OIDC) comes from: `{"command": [...]}`,
`{"env": NAME}` or `{"file": PATH}`. The pusher fetches one when it needs
access and trades it for short-lived credentials: AWS
`AssumeRoleWithWebIdentity`, Google workload identity federation, or a
Microsoft Entra federated credential.

Pushing needs permission to create and replace objects under the prefix.
The pusher never reads, lists or deletes. On S3 that is `s3:PutObject`. On
Google Cloud Storage replacing an object needs `storage.objects.delete` as
well as `storage.objects.create`, so a create-only role fails the first
time a chunk is sent again; on Azure a SAS needs create and write.

Other fields:

- `prefix`: the folder inside the bucket. It may contain `{owner}`
  ([below](#whose-work-it-is)).
- `retention_days`: the bucket's lifecycle, if it has one. The pusher
  uploads a chunk again once half of it has passed, so a conversation that
  is still growing never loses its beginning.
- `pool`: the pool this machine belongs to. On a recognized platform it
  defaults to the platform and environment, such as "Niteshift · api".
- `sources`: folders to capture, as `{"name", "kind", "path"}`. Without it
  the pusher uses the usual folders of each kind it can push (`claude`:
  `~/.claude/projects`, `~/.config/claude/projects`; `codex`: `~/.codex`),
  with `CLAUDE_CONFIG_DIR` and `CODEX_HOME` first when they are set.

## State

The state folder is `~/.pharos/push`, or `--state` or `PHAROS_PUSH_STATE`:

| Entry | Holds |
|---|---|
| `captures/` | The capture root: the same layout Pharos writes |
| `state.json` | The pusher ID, each source's newest export, which chunks the bucket has and when they were sent, and each captured file's chunk list |
| `push.lock`, `pending` | One pass at a time, and a request made during one |
| `push.log` | One JSON line per pass; `--status` shows the last |

Removing the folder is safe. The next pass captures and uploads again as a
new pusher, and the library reads both as copies of the same conversations.

Do not bake a state folder into a machine image. If one is found on a
machine it was not made on, it takes a new pusher ID and a new host before
it pushes. On a recognized platform "machine" means the platform's task; on
a Mac it is the hardware UUID, so a hostname that follows the network does
not make a new pusher; elsewhere it is `/etc/machine-id` together with the
hostname.

The captured host is `PHAROS_HOST_ID` when set, and otherwise follows the
pusher ID. Its label is `PHAROS_HOST_LABEL`, or the hostname.

## What a pass does

1. Captures every source into `captures/`. Only changed files are copied.
2. Writes `hello/<pusher>.json` if this pusher has not yet.
3. For each source, reads the capture as
   [`capture-export`](capture-export.md) would, with the same checks, and
   cuts each file into content-defined chunks (`gear-v1`). A chunk the
   bucket already has is not sent. A file that has not changed since the
   last pass is not read.
4. Writes the export, `exports/<pusher>/<source>/<time>-<id>.json`, after
   its chunks. It holds the capture-export envelope, each file's chunk
   list, and the export it follows.

A source whose transcripts have not changed makes no export and writes
nothing. A pass that fails partway leaves only chunks, which the next pass
reuses; a reader never sees an export whose chunks are missing.

No chunk or export key is ever written with different bytes. The `hello`
object is the exception: `--check` rewrites it.

Passes for one state folder run one at a time. A request made while one
runs is served by one more pass when it finishes, so the newest bytes are
always in the last pass. Background passes start at least 30 seconds apart.

If the destination refuses five passes in a row (a revoked key, a removed
bucket), background pushes drop to one attempt a day and `--status` says
so. `--wait` and `--check` always try, and a successful `--check` ends the
pause. Network failures are not refusals and are retried on the next pass;
that includes a sign-in host (the token exchange of keyless access) that
cannot be reached or answers 5xx or 429.

## Whose work it is

A team can send every sandbox to one bucket and have each person collect
only their own conversations, by putting `{owner}` in the prefix. The owner
is, in order:

1. `owner` in the configuration, or `PHAROS_PUSH_OWNER`;
2. `shared`, when the machine is declared to run an automation: `automation`
   in the configuration, or `PHAROS_PUSH_AUTOMATION`, naming it (for
   example "Sentry autofix");
3. the platform's identifier for the person the task was started for.

If the prefix has `{owner}` and none of these gives one, the push is
refused with a message. It never guesses a person and never files work
under `shared` unless told the machine runs an automation.

## Platforms

The pusher recognizes the platform it runs on from environment variables,
and records the platform, environment, task and task link in its hello and
exports. The recipes are in
[`contracts/plugins/platforms.json`](../contracts/plugins/platforms.json),
which a Go test holds equal to `pushPlatforms`; run it with
`PHAROS_UPDATE_GOLDEN=1` after changing one.

| Platform | Recognized by | Owner | Identity token for keyless access |
|---|---|---|---|
| Niteshift | `NITESHIFT_TASK_ID` and `NITESHIFT_SANDBOX_ID` | `NITESHIFT_USER_ID` | `ns oidc-token --audience AUDIENCE` |

Where the owner's variable is not set (Niteshift does not give it to a
supervised service), the pusher reads the same person from the identity
token's `user_id` claim. A Niteshift task with neither is refused when the prefix
has `{owner}`, until `PHAROS_PUSH_OWNER` or `PHAROS_PUSH_AUTOMATION` says
what it is. See [the platform design](plugin-import-platforms.md) for the
reasons and for what is still unchecked.

## Using it from an agent hook

In Claude Code's settings, for example:

```json
{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "pharos capture-push"}]}]}}
```

The command returns as soon as the worker is started and exits 0 whatever
happens, so it cannot slow or fail a turn. Where a platform owns the
agents' settings files (Niteshift does), use `--watch` as a supervised
service instead of editing them.

## Collecting into a library

```text
pharos --config LIBRARY_CONFIG bucket-import --from pharos-read.json
pharos --config LIBRARY_CONFIG index --host HOST
```

`pharos-read.json` is the same contract with `"access": "read"`, a
credential that can list and read, and a prefix naming one owner's folder
(no `{owner}`). `bucket-import` runs as the `capture.import` operation,
variant `bucket`:

1. Lists `exports/` and takes each pusher's newest export per source. Each
   export holds everything its predecessors did, so the ones between two
   runs are skipped.
2. Rebuilds the export in a scratch folder, fetching chunks, and verifies
   it whole with the capture-export verifier. A file the previous export
   already brought unchanged is copied from the capture root instead of
   fetched.
3. Merges it into `<capture root>/<host>/<source>`: the pushing machine is
   one more captured host. Files are written first and the manifest last.
4. Prints what it collected and the hosts to index. Indexing is the
   ordinary capture index; the app's background capture-and-index pass
   already indexes every host under the capture root.

Collection only adds. An export that fails verification changes nothing.
An export that lacks a file the library already holds from that machine is
refused whole and reported. A host folder that was not collected from a
bucket (a Mac's own capture) is never merged into, and an export claiming
to be this Mac is refused. The marker that says a host folder came from a
bucket is written before its first file, so a first collection that stops
partway is completed by the next run. The cursor is in
`<capture root>/.bucket-imports/<destination>/state.json`; a host collected
this way has a `.bucket-import.json` recording its pusher, owner, pool and
platform.

Code: `internal/archive/capture_bucket_import.go`; tests beside it.

## Setting up Niteshift with Google Cloud Storage

**Settings → Sources → Collect from other machines** writes this setup from
four names (the Google Cloud project, its number, a new bucket's name, and
the Niteshift organization ID) and connects the library at the end. The
**Collect from other machines** item on the Background Processing page
links to it while nothing is connected. It is the one combination that has
been run end to end; other clouds and platforms use the same pieces,
configured by hand from the sections above.

| Step | Where | What it does |
|---|---|---|
| 1. Create the bucket and its access | Terminal on the Mac, `gcloud` signed in as a project administrator | Creates a private bucket whose objects expire 90 days after their last upload; a custom role `pharosPusher` that may create and replace objects but not read or list them; a workload identity pool `pharos-push` with a provider `niteshift` that accepts only task tokens from the one Niteshift organization; and read access for the signed-in Google account. Prints the project number |
| 2. Add three files to the repository | The default branch of each repository | `.niteshift/services.yaml` starts a supervised service in every task. `.niteshift/pharos-push.sh` downloads the release's Linux program if it is missing, checks its SHA-256, and runs `capture-push --watch`. `.niteshift/pharos-push.json` is the push configuration: prefix `pharos/{owner}`, keyless access with `ns oidc-token` |
| 3. Check from a task | A Niteshift task's terminal | `ns services status`, then `sh .niteshift/pharos-push.sh --check`, which prints `"ok": true` and the task owner's `prefix` |
| 4. Connect the library | Settings → Sources | Saves a read configuration for that prefix, with `gcloud auth print-access-token` as its credential, after reading the bucket once. A configuration that cannot read the bucket is refused |

Nothing secret is stored anywhere: a sandbox trades its Niteshift identity
token for short-lived Google access, and the Mac uses the person's own
`gcloud` sign-in. Each person's tasks push under their own
`pharos/<Niteshift user ID>` folder and each person connects their own
folder. Any task in the organization can replace objects in the bucket,
but cannot read them.

The guide is `internal/archive/assets/bucket-setup.js`.
`testdata/bucket-setup.json` holds the two configurations it writes; a
JavaScript test holds the guide to that file and a Go test holds the file
to `readPushConfig` and the connect check, so the screen cannot write a
configuration the commands refuse. The read configuration in step 4 can be
edited or replaced, which is how a bucket set up by hand (S3, or another
platform) is connected from the same screen.

A credential command such as `gcloud` is run by name. The Mac app does not
inherit Terminal's `PATH`, so a name the service cannot find is looked up
on the login shell's `PATH`.

### As a Background Processing item

Connecting a bucket makes collection a scheduled item, **Collect from other
machines** (Hourly by default; its schedule is changed like any other
item's). Each run collects from every connected bucket, then indexes the
hosts that received something. Until a bucket is connected the item is
unavailable.

```text
pharos --config LIBRARY_CONFIG bucket-import --from pharos-read.json --save   # connect, and collect once
pharos --config LIBRARY_CONFIG bucket-import                                  # collect from every connected bucket
pharos --config LIBRARY_CONFIG bucket-import --list
pharos --config LIBRARY_CONFIG bucket-import --remove ID
```

The service does the same over its API: `GET /api/bucket-imports` lists
the connected buckets without their credentials, each with the number of
machines collected from it and when it was last collected.
`POST /api/bucket-imports` takes `{"config": READ_CONFIGURATION}` or
`{"remove": ID}`. Connecting, from the command or the API, first lists the
bucket's `hello` folder with the configuration; a failure is reported and
nothing is saved. The API's answer says how many machines have checked in,
and asks for a collection at once. A connected configuration holds this Mac's credential, so
it is kept in this Mac's support folder (`bucket-imports/`), not in the
library; files it names must be absolute paths. Removing one keeps what it
collected.

### Machine and Channel

A collected host's **pool** is its machine: every sandbox of
"Niteshift · api" is one machine of kind `cloud`, however many there were.
A host with no pool is a machine of its own. The **platform** an export
names is the Channel of that host's conversations (`niteshift`), ranked
with an explicit initiation, so it outranks what the agent app implies; a
conversation's own recorded initiation still wins. Both are applied when
the host's captures are indexed.

## Limits

- `bucket-import` reads Google Cloud Storage and S3 (and S3-compatible
  stores). Azure and plain-URL destinations can be pushed to but not yet
  imported from.
- The Settings guide writes one setup: Niteshift with Google Cloud Storage.
  Other combinations are configured by hand and connected by pasting their
  read configuration.
- The release download in `.niteshift/pharos-push.sh` has not been run in a
  sandbox yet: no release has carried the Linux program. A sandbox keeps
  the program it downloaded; a new task downloads the newest.
- A read credential that needs `gcloud` stops working when that sign-in
  expires; the item then fails with the command's error until the person
  signs in again.
- The owner and initiator an export carries are recorded beside the host
  but not shown; an automation's runs are not yet marked with the
  `automation:<name>` sender.
- It follows each pusher's newest export only. An export that the newest
  does not descend from (two machines sharing one pusher ID) is not yet
  found.
- An interrupted merge can leave a file newer than the manifest describes
  until the next run completes it.

- Only kinds that can travel as a capture export are pushed: Claude Code
  and Codex. Codex SQLite record capture is not included.
- Detaching the worker is implemented for macOS and Linux. Elsewhere the
  default form pushes before it returns.
- The Linux build compiles and its code paths are covered by the shared
  tests, but it has not yet been run on a Linux host or inside a Niteshift
  task.
- Chunks are uploaded one after another, so the first push of a long
  history is slow. Later pushes send only what changed.
- Releases do not yet publish a Linux binary.
