# Risky action notices in Optimize

Optimize has two sections: **Efficiency** and **Risky actions**. Both collect ideas in
persistent prompt baskets. Each basket is for a repository or Mac; safety and
efficiency can never share a prompt, even when they name the same repository.

Each section has **Dashboard**, **Table**, and **Settings** pages. The Risky actions
Dashboard keeps the compact cards and baskets. Its Query Table lists the same
notices across all states, with selected resources, checks, locations, call
counts, dates, and handoff state. It supports the shared filter, grouping,
charting, and saved-query controls. **Open notice** opens Details while retaining
the table query for the return trip.

Many notices of one check, such as a deletion outside the checkout or a
permission change, are easier to read as one list, so the dashboard shows one
card per check for the shown list and location, in two columns beside the
prompt baskets; individual notices live in the table. A card leads with what
its notices share before what varies: its danger, **Recent** when a notice was
seen in the last three days, its count,
the one location they are all in (or the busiest locations, with the rest
counted), a directory every selected path is under, the most common resources
with repeat counts, then the span of activity, calls, notices with new
evidence, and notices already in a prompt. Paths read from `~`, and Git
subjects show the operation, since the location already names the repository.
Each card's checkbox selects all of its notices for the same actions as table
rows, so a whole check can be copied as a prompt (one per location) or
acknowledged and closed at once. The card's link opens the table filtered to
that check (`rule`), with the same state and location (`target`). The table
then starts with the check's explanation and questions, keeps the query builder
collapsed (`query_layout`), and lists each selected path beside the latest
command that selected it and its working directory, sorted by path. Details
links to every notice of its check. MCP's `list_safety_cases(rule=…)` returns
the same selection with each notice's `subject`.

Table rows have checkboxes. A selection can be acknowledged and closed (moving it to
Closed), brought back, or
added to prompt baskets at once (`POST /api/findings/safety/bulk`); any
unavailable or ineligible notice rejects the whole selection. A prompt asks an
agent working in one location, so a selection is planned per location, and a
note under the toolbar says how. **Copy prompt** opens the usual prompt review
for the selection's location; across locations it lists one prompt per
location (up to 50 notices each) to copy in turn. **Add to prompt** adds each
notice to its location's basket and names the baskets it filled. When notices
are not open, the note says so instead of leaving the buttons silently disabled.

Each edit to the table's query (filters, sorts, columns, paging) is its own
browser history step. After filtering, selecting and closing a set of
notices, which leaves the filtered table empty, Back returns to the query
before the filter.

The dashboard shares Efficiency's state tabs, counted Everywhere filter, and
segmented Rank by controls. Check cards rank by **Danger** (the default),
**Recent** (latest activity), **Calls**, or **New evidence** (notices with new
execution evidence); ties fall back to danger. The Optimize badge counts each
check with open notices once, as the dashboard shows it. Danger is set per
check, not scored per notice:

- **High** (chip: **Dangerous**): credentials copied, published Git history rewritten, cloud
  objects deleted, disk images or volumes detached or changed, and system or
  disk changes. These are rarely routine agent work.
- **Medium** (chip: **Risky**): deletions outside the checkout, database changes outside the
  checkout, broadened permissions, the checkout or ignored files deleted, and
  published releases deleted or replaced.
- **Low** (no chip, so a chip always means something): shared Git work discarded, every instance of a program stopped, and
  remote API changes.

Settings and the overview always lead with high danger, so a rare credential
copy outranks a common process signal. Cards and Details show the chip, and the table's **Danger** column sorts
by severity (`danger_rank`).
`safety_rank` and `safety_place` restore the selected order and location from a
link independently of Efficiency's saved ranking.

Risky action settings enables or disables individual discovery checks and offers an
explicit refresh. Disabling a check stops future discovery/evidence updates;
existing notices, baskets, and handoff history remain available. Re-enabling a
check lets the next full refresh examine its retained evidence again. These
switches use `safety_rule_off:*` keys in the library's existing settings store.

Each check states what happened (for example "Credentials copied to another
location") rather than asking for a review. A notice's exact subject, commands,
investigation questions, and archived source links live in Details, opened from
the table, with **Add to prompt** and **Acknowledge and close** for that one notice. The wording never equates a selected command with
harm, authorization, or a confirmed effect.

## Handoff

Each location has one prompt basket, shown two ways. On the dashboard, where a
card is the unit of work, the basket lists each check once with how many of its
notices it holds; ticking or removing a check ticks or removes all of them
(`POST /api/findings/safety/bulk` with `cart_tick` or `cart_remove`). The table
shows the same basket per notice, beside the table, for choosing single
notices.

The per-notice basket supports selecting, ordering, removing, and moving notices
between risky action prompts. The default target is the repository, then the source
Mac, and finally the workspace when its machine is unknown. Risky action baskets do
not clone an item; one notice has one current handoff.

Preview changes no state. **Copy prompt and hand off** copies the selected
notices' investigation instructions and records their handoff. The selected
items leave the basket and Open immediately. Unticked items remain. A matching
preview token prevents recording a different selection from the one reviewed;
successful retries are idempotent. Clipboard failure records nothing. A failed
recording is shown explicitly so the user can review and retry.

The prompt asks the agent to investigate, explain, and recommend fixes. It
states each check once: its explanation, its questions, the fixes to consider
and its notice IDs. Most prompts hold one check, so the prompt opens by naming
it and its danger instead of repeating per-notice instructions; a prompt that
mixes checks has one section per check, most dangerous first. The agent groups
notices that share a cause and concludes once per group, with citations and
benign explanations as well as concerns. Each check names its fixes: the safer
or established way to do the same work (for credentials, point the tool at the
original store or mint a scoped token), else a stronger prohibition. The agent
proposes them, with exact wording for agent instructions or permission rules,
and changes nothing unless asked after its report. Source commands, paths, and transcript
payloads stay off the clipboard: `get_safety_case` supplies source handles and
questions, and `get_conversation_messages` supplies surrounding evidence.
Archived content remains untrusted evidence throughout.

Copied cases remain in **Handed off**, with **Bring back** available. No returned
report or resolution confirmation is required. Handed off records the user's
handoff, not a claim the underlying concern was resolved. Efficiency retains
its existing measurements, results, and savings.

## Selection and repeats

Discovery runs with a full Findings refresh and reads retained shell calls in
the last 28 days, excluding mirrored workspaces. It uses archived full tool
inputs when available and the existing shell splitter, which skips comments,
quoted strings, and heredoc data. It never runs archived commands or inspects
the current filesystem. A single event can qualify; efficiency's recurrence,
savings, and observability gates do not apply.

A notice is meant to say "look into this", not to catalog everything that
might be risky. Checks therefore skip what an agent routinely does to its own
work, and keep what is likely to need the user:

- **Deletions** skip temporary roots (`/tmp`, `$TMPDIR`, `mktemp` results),
  build and cache directories (`.build`, `node_modules`, `dist`, `.context`,
  worktree scratch, and so on) and scratch file names. Inside the checkout only
  the checkout itself and `.git` count. A target that cannot be resolved counts
  only when it could expand to a whole directory (`rm -rf "$DIR/"`, `/*`, `~`).
  Variables assigned earlier in the same command are expanded.
- **Databases**: SQLite files outside scratch space, and `psql`/`mysql`
  against a named remote host. Local or variable hosts are skipped.
- **Remote history**: unconditional force pushes, mirrors and ref deletions,
  and lease pushes to shared branches (`main`, `master`, `release*`, …).
- **Local history**: force-deleting a shared branch, clearing the stash, or
  dropping a stash entry by position; every worktree shares one stash.
  Resets, rebases and an agent's own branches are skipped.
- **Git clean** only with `-x`/`-X`, which can remove ignored secrets and data.
- **Releases**: deleting a release or replacing assets (`--clobber`). Merges,
  tag pushes and new releases are routine.
- **Processes**: a selector that names a program (`pkill -f vite`), not a
  path, port, test name or generated ID.
- **Permissions**: modes writable by others or setuid/setgid, and ownership
  changes outside the checkout.
- **Disk images**: detach, eject, resize or convert of an existing image or
  volume. **System**: disk changes, not `diskutil list`/`info` reads.
- **HTTP**: mutations of non-local services, except search and lookup POSTs.

Recognized help and dry runs never raise notices. These are heuristics, not
independently validated incident findings or complete coverage of every
action. `TestSafetyBackupProbe` (`-tags probe`) measures a change against a
backup's retained calls; on one library's 28 days the sharper checks raised 76
notices where the earlier ones raised 2,557.

Discovery records its version (`safety-v3`) on each case. A refresh removes
cases an earlier version selected that this one no longer does, unless the user
closed, handed off, or added them to a prompt. A disabled check is not run, so
its cases stay until it is enabled again.

Unlike the offline `tools/risky_tools` research passes, this first product
implementation does **not** interpret embedded Python ASTs, arbitrary native or
MCP mutations, indirect scripts, or all shell syntax. Code Mode recovery is
limited to the ledger's supported literal command forms. Dynamic values remain
unresolved; unresolved resource identities are scoped to the source call. The
coverage disclosure names these limits. An empty queue is not an assurance of
safety. The original research reports and annotations remain separate.

Cases have stable identities based on rule, prompt scope, source host (workspace
when unknown), selected resource, and operation breadth. Repeated activity in
the same case accumulates without undoing a handoff. A different destination,
scope, or broader operation creates another case. New evidence of successful
execution of a single command can reopen a previously unconfirmed handed-off
case. Compound calls and background launches never establish completion of each
segment. Exit zero still does not establish affected contents or harm.

Git cleanup identities retain path restrictions, exclusions, ignored-file mode,
and whether force permits deleting nested repositories. A wider cleanup therefore
needs its own handoff. Git repository/ref expressions that cannot be resolved
remain scoped to their source call. GitHub CLI operations without an explicit
repository use the effective working directory; a missing PR identifier also
remains unresolved instead of assuming the current branch is unchanged.

Force-push identities retain bulk selection (`--tags`, `--all`, `--mirror`),
tag-following, pruning, and explicit lease scope. Process identities retain the
program, matching options, user/group/parent restrictions, and signal. Rsync
deletion identities retain source paths (including trailing-slash semantics),
ordered filters and options, and deletion modes. Referenced filter/list files,
PID files, and dynamic selections remain scoped to the source call because their
historical contents are unknown.

Directory tracking separates successful and failed `cd` paths and keeps subshell
directories local. `cd /outside || rm -rf .git` therefore selects the original
directory; `(cd /outside && echo ok); rm -rf .git` also uses the outer directory.
When paths converge with different directories, or unsupported control flow or
shell-dependent pipeline builtins prevent a reliable inference, relative targets
remain unresolved and select nothing. Absolute targets can still be identified.

Option parsing consumes supported option values and respects `--`: a body or
filename spelling `--help` or `--dry-run` does not suppress a mutation. Direct SQL
arguments are checked across statement boundaries while quoted strings,
identifiers, and comments are masked. A preceding transaction or read does not
hide a later mutation. This remains lexical inspection, not database execution
or complete SQL interpretation.

Details retain up to 20 examples and preserve an example supporting stronger
execution evidence. User dispositions and handoff history survive rediscovery.
They live in `safety_state`, `safety_cart`, and `safety_handoffs`; discovery writes
`safety_cases`. The tables are library-local and included in ordinary backups;
history moves preserve the user-state tables separately from derived evidence.

## API and links

`/findings?optimize_area=safety` opens Risky actions. `safety_list` selects `open`,
`handed_off`, or `dismissed` (shown as Closed); `safety_place` selects a stable target; `safety_case`
opens Details. Evidence and coverage disclosures use shared URL view state.
`view=table` opens its table (`q_safety`); `view=settings` opens Risky action settings.

```text
GET  /api/findings/safety
GET  /api/findings/safety/{id}
GET  /api/findings/safety/settings
POST /api/findings/safety/settings
POST /api/findings/safety/{id}/action
POST /api/findings/safety/bulk
POST /api/findings/safety/cart/prompt
POST /api/findings/safety/cart/copy
POST /api/query/safety/rows-v2
```

MCP exposes `list_safety_cases(state?, rule?, limit?)` and `get_safety_case(id)`.
Reading changes no state. Both copy APIs reject mixing safety and efficiency
IDs. The Optimize badge includes open notices from both sections, while each
section keeps its own counts and baskets.

The interface calls this section **Risky actions**. Existing `safety` dataset,
URL parameters, API routes, and MCP method names remain stable for saved links
and agent integrations. The sample preview shows all 13 discovery checks, with
three illustrative notices; notice count does not limit the available checks.
