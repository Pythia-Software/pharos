# Canonical source export

An export is one workspace object or `{"workspaces": [...]}`. Unknown keys are retained by the producer but safely ignored by this extractor.

```json
{
  "id": "stable-source-workspace-id",
  "source_kind": "tl1",
  "title": "Human-readable purpose",
  "purpose": "Initiating goal",
  "outcome": "Observed result",
  "activity_at": "2026-09-18T12:00:00Z",
  "location": "/exact/worktree/path",
  "repository": {
    "canonical_remote": "https://github.com/owner/repo.git",
    "display_name": "repo",
    "owner": "owner",
    "aliases": [],
    "local_locations": []
  },
  "metadata": {
    "base_ref": "commit", "head_ref": "commit", "branch": "branch",
    "owner": "owner", "flavor": "candidate", "version": "shape-v1",
    "lifecycle": "terminal", "terminal": true, "custody_version": "v4"
  },
  "conversations": [{
    "id": "provider-native-session-id", "provider": "codex", "parent_id": null,
    "model": "gpt-6", "effort": "high",
    "aliases": ["conductor-declared-native-id"], "coverage": "complete",
    "messages": [{
      "id": "provider-event-id", "role": "user", "kind": "message",
      "text": "Visible content", "created_at": "2026-09-18T12:00:00Z"
    }]
  }],
  "changes": [{
    "classification": "attempted", "base_id": "…", "head_id": "…",
    "files": [{"path": "src/file.py", "status": "modified", "tracked": true}]
  }],
  "metrics": [{
    "name": "input_tokens", "value": 1200, "unit": "tokens",
    "status": "reported-cumulative", "coverage": 1.0, "extractor_version": "tl1-v1"
  }],
  "prs": [{
    "host": "github.com", "number": 42, "relationship": "commit-evidence",
    "confidence": 1.0, "evidence": {"commit": "…"}
  }]
}
```

Roles and metric absence are preserved. Producers should classify subagent tool calls as `kind: "delegation"`; they are retained even though routine tool traffic is low priority. Never emit hidden reasoning.


Conversations and messages accept optional `model` and `effort` fields. A message inherits the conversation effort when its field is absent; `effort: null` explicitly means unknown. Set effort on each message when configuration changes during a conversation. Levels are retained as normalized lowercase strings, including provider-specific and future values. Pharos does not infer effort from thinking token counts or thinking budgets.

Conversations also accept optional initiation metadata:

```json
{
  "id": "provider-native-session-id",
  "provider": "codex",
  "initiation": {
    "channel": "desktop",
    "machine": {"id": "device-laptop", "label": "Laptop", "kind": "device"},
    "execution_machine": {"id": "worker-1", "label": "Remote worker", "kind": "cloud"},
    "evidence": "Recorded initiating client and worker assignment"
  }
}
```

Channels are `conductor`, `tl1`, `cli`, `desktop`, `web`, `ide`, or `api`; omit
unknown values. Machine kinds are `device`, `web`, and `cloud`. IDs must be stable
within the library; names can change without changing IDs. A known Web origin
without a device is displayed as **Web (device unknown)**. An export without
initiation metadata keeps unknown placement rather than inheriting the importing
Mac.

Each descendant inherits the initiating root's channel and machine, including
mixed-provider and remote delegation. `execution_machine` describes the session
itself and can differ from the root's machine. Preserve native parent IDs and
aliases so that children imported before their parents can resolve later.
`parent_id` names the parent's native ID; optional `parent_provider` and
`parent_account` identify parents from another provider or account and default
to the child's provider and source account. A
child's runtime does not override its parent's channel. Conflicting equal-authority
claims remain visible as conflicts. See [channel and machine](channel-and-machine.md)
for inheritance, evidence, and grouping semantics.

## Inference speed

Conversations and messages accept `speed: "fast" | "standard"` and an optional
`service_tier` string. Message settings override conversation settings; a null
tier defaults to standard when no explicit speed is set. OpenAI's `priority`
tier (requested as `fast`) and its `ultrafast` tier are fast. All other or
missing tiers default to standard; an explicit speed field takes precedence so
Claude's `speed: "fast"` works alongside `service_tier: "standard"`.
Normalization and streamed request merges update speed and tier independently:
tier-only accounting preserves an explicit speed until another speed setting or
reset arrives. A null or unknown speed clears that override and uses the tier.

Ingestion normalizes the settings on each retained message and model request:

| Field | Values | Meaning |
|---|---|---|
| `speed` | `fast`, `standard` | Stable dimension for filtering and grouping. |
| `service_tier` | Original tier, trimmed and lowercase; otherwise null | Recorded routing/configuration evidence. |
| `speed_source` | `speed`, `service_tier`, `assumed_standard` | Distinguishes a recorded setting from the standard-speed fallback policy. |

Codex turn context, Claude request usage, Conductor accounting/configuration,
ChatGPT message metadata and canonical messages share this contract. It survives
stored-message replay and catalog recovery. The versioned request-ledger rebuild
backfills retained evidence; discarded original settings require source reindexing.
`model_requests` exposes these dimensions to native queries and includes
`model_speed` and `model_effort_speed` convenience grouping fields. Request token
counts and observed timing remain separate measures; speed never implies effort.
