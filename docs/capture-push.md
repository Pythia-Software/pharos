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

The library side that reads the bucket (`bucket-import/1`) is not built yet;
see [the import design](plugin-imports.md#order-of-work).

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
| `gcs` | `service_account_key`, or an HMAC key as `access_key_id` and `secret_access_key` | `workload_identity_provider`, `identity_token`, and optionally `service_account` to act as |
| `azure` | `sas_url` for the container | `account`, `container`, `tenant_id`, `client_id` and `identity_token` |
| `http` | `url_template` containing `{key}`, and optionally one `header` | |

**Keyless** means nothing secret is stored on the machine. `identity_token`
says where a signed identity token (OIDC) comes from: `{"command": [...]}`,
`{"env": NAME}` or `{"file": PATH}`. The pusher fetches one when it needs
access and trades it for short-lived credentials: AWS
`AssumeRoleWithWebIdentity`, Google workload identity federation, or a
Microsoft Entra federated credential.

Pushing needs permission to create objects under the prefix and nothing
else. The pusher never reads, lists or deletes.

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
others it is `/etc/machine-id` together with the hostname.

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
pause. Network failures are not refusals and are retried on the next pass.

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

A Niteshift task with no `NITESHIFT_USER_ID` is refused when the prefix
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

## Limits

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
