# Conversation and work names

The Library's Work column uses the most specific useful name available for a
work. Names resolve during indexing and human-authorship maintenance, and the
same stored display name appears in Library, search, conversation details and
exports.

## Choosing a name

A useful name supplied by the source takes precedence. Empty names, names equal
to a source ID, provider labels such as `Codex a83f12bc`, and placeholders such
as `Untitled` or `New chat` can use a fallback.

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
when shortened. It is an excerpt, not an AI-generated title. If no typed words are
available, the original fallback remains.

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

Independent unit audits verify the stored source title and scope separately from
the resolved display name, which can depend on other works or global authorship
classification. Audit evidence includes the naming indexes and database snapshots
used by the parser, with the same stability checks and byte budget as transcripts.

Catalog schema 14 separates source and display names. Older builds cannot write
this catalog, which prevents them from overwriting resolved names without
updating their provenance. See the [original naming survey](conversation-naming-survey.md)
for the coverage measurements that motivated these rules.
