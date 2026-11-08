# Conversation and work names

The Library's Work column names a workspace; a conversation's primary name is
chosen by the shared agent-session resolver. Source titles retain their original
scope. A workspace containing several attempts or helpers does not give them
all its broader name.

## Choosing a name

A useful name supplied by the source takes precedence. Bare UUIDs, identifier-only
hashes, internal entity IDs, transcript paths, provider labels such as
`Codex a83f12bc`, and placeholders such as `Untitled` or `New chat` are weak names,
even when the identifier differs from the row's own source ID.

For an unnamed work, Pharos checks declared native session identity links for
names from another captured source. A native conversation name is more specific
than a Conductor session name; both are more specific than a Conductor workspace
or TL1 task name. It does not infer identity from a common directory or branch,
and it does not propagate borrowed names across a task's other conversations.
Several equally specific names that agree are usable. Conflicting names retain
the previous meaningful choice; with no previous choice, Pharos uses its cached
human-word fallback or retains the source's placeholder.

When no useful source or linked name is available, Pharos uses the first message
containing words classified as **typed** by the shared human-authorship
algorithm. It excludes harness instructions, automation, templates, pasted
output, quoted agent text and attachment references according to those same
rules. The label contains up to 12 words and 80 text characters, plus an ellipsis
when shortened. It is an excerpt, not an AI-generated title. The first human
words anywhere in the conversation take precedence over an automated opening.

When no human-written words are available, a recorded launch assignment or an
automated/agent request can supply an excerpt. This does not change its
human-authorship classification. Harness blocks, environment instructions,
attachment wrappers, encrypted payloads and command output do not supply names.
Readable nicknames remain available; task context is used only for the task's
own attempts. A TL1 attempt can use `task title · attempt N` when its recorded
native conversation reference identifies that attempt unambiguously.

Primary names never fall back to a native ID, path or a fabricated provider/date
title. A conversation with no usable retained request, name, nickname or task
context shows **Conversation without retained request**. Native IDs remain in
the API's `native_id` metadata and remain searchable.

Explicit source renames update the display name. A meaningful title explicitly
changed in the catalog is preserved as an override. This does not add a new
rename interface. Derived excerpts remain stable when later turns are added;
editing or deleting their originating message invalidates that cached excerpt.

## Capturing source names

Codex sources include `session_index.jsonl` and snapshots of known `state*.sqlite`
databases with a `threads.name` column. The newest numbered state schema takes
precedence over older schemas and the append-only session index, including when
its name has been cleared. `threads.title` is not used as a semantic title: it
can contain the raw first prompt. Old transcript-only captures cannot recover
metadata that was never captured.

Claude sources retain `ai-title` events and recognize `custom-title` events.
An explicit transcript title takes precedence over later automatic titles and
stale session-index metadata. Captured `sessions-index.json` files provide custom
titles or summaries for existing transcripts; orphan index entries do not
create conversations. An unreadable or malformed project index is skipped for
that directory; its transcripts still index and a repaired index is read on the
next source change check.

Conductor session names are preferred over its broader workspace names. A
workspace name fills a placeholder session title only when it differs from the
workspace directory name. TL1 task names, Antigravity summary/annotation names,
and titles supplied by ChatGPT or canonical exports continue to be used.

Name metadata participates in source fingerprints and per-transcript dependency
signals, so a title-only change is visible without a change to message content.
Portable captures retain the metadata needed to apply the same rules on another
Mac, and older captures do not overwrite a newer recorded source read.

## Caching and provenance

`workspace_names` keeps the original source title and scope apart from the
resolved `workspaces.title`. It records the selected title's origin, the source
workspace when borrowed, and any explicit override.

The same table caches the first typed-word excerpt with its originating message
ID, content hash, timestamp and classifier/naming version. The existing
human-authorship pass fills it while it already has the classified spans in
memory. A one-time upgrade can fill it from saved spans when that classification
is current. Stale classifications wait for the existing authorship refresh.

Library reads use the stored display name and never run classification or scan
message text to construct a name. A title-only source edit does not invalidate
the expensive authorship analysis. A changed message hash or naming/classifier
version prevents a stale excerpt from being reused.

`conversation_name_candidates` stores each conversation's first human excerpt
and first recorded request excerpt separately, together with the source message,
content hash, sender, timestamp, workspace, classifier source hash and naming
version. Refined classifier spans are reused only when their source hash and
version still match. Dirty or provisional classifications wait for ordinary
authorship maintenance. Agent-role assignments have their own provenance and
never become human-word counts.

The resolver reads only compact metadata and validated cached excerpts. A scoped
source title or explicit override wins; human text is the first excerpt fallback,
followed by launch/request text, nickname and task context. Mirrored and continued
recordings share one primary name in the heading, picker, Hierarchy, Timeline and
related-session links. Reader selection and filters never choose a different name.
Delegation API rows expose the chosen provenance as `label_source`.

`conversation_names_state` is a durable dirty queue. Opening an upgraded catalog
queues existing conversations without reading every transcript. Search maintenance
(`maintenance.conversation-names`) processes batches of at most 25 conversations
and approximately one second of writer time, checkpointing only committed work.
It runs under both execution engines and honors Search refresh Stop/Resume.
Message edits/deletions, ownership changes and classifier publication requeue the
affected conversations. An edited source cannot supply its old cached name while
that queue catches up. A single-conversation workspace can reuse its request
excerpt; multi-conversation workspace titles keep their own scope.

Name-only source edits, changed source scope, candidate publication and classifier
freshness changes invalidate the shared execution cache. The naming policy is
versioned independently of the classifier, so a naming upgrade does not force a
full human-authorship reclassification or provider reingest.

### Read-only audit

Preview candidate names without initialization, catalog writes or source edits:

```sh
pharos --config /path/to/library.toml conversation-names audit --limit 100
pharos --config /path/to/library.toml conversation-names audit --conversation conversation_ID
```

The audit reports current/proposed primary names, provenance, changed/preserved
counts and conversations without a usable retained request. The default scope is
100 conversations; `--limit 0` examines all retained conversations. It reuses the
production candidate extractor and shared resolver in one read-only snapshot.
The automatic backfill resumes through normal Search maintenance after deployment.

Independent unit audits verify the stored source title and scope separately from
the resolved display name, which can depend on other works or global authorship
classification. Audit evidence includes the naming indexes and database snapshots
used by the parser, with the same stability checks and byte budget as transcripts.

Catalog schema 14 separates source and display names. Older builds cannot write
this catalog, which prevents them from overwriting resolved names without
updating their provenance. See the [original naming survey](conversation-naming-survey.md)
for the coverage measurements that motivated these rules.
