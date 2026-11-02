# Risky action reviews in Optimize

Optimize has two sections: **Efficiency** and **Risky actions**. Both collect ideas in
persistent prompt baskets. Each basket is for a repository or Mac; safety and
efficiency can never share a prompt, even when they name the same repository.

Each section has **Dashboard**, **Table**, and **Settings** pages. The Risky actions
Dashboard keeps the compact cards and baskets. Its Query Table lists the same
reviews across all states, with selected resources, checks, locations, call
counts, dates, and handoff state. It supports the shared filter, grouping,
charting, and saved-query controls. **Open review** opens Details while retaining
the table query for the return trip.

The dashboard shares Efficiency's state tabs, counted Everywhere filter, and
segmented Rank by controls. Risky action reviews rank by **Recent**, **Calls**,
or **New evidence**; call counts appear on cards when ranking by Calls. Ranking
uses retained activity and execution evidence, without assigning a risk score.
`safety_rank` and `safety_place` restore the selected order and location from a
link independently of Efficiency's saved ranking.

Risky action settings enables or disables individual discovery checks and offers an
explicit refresh. Disabling a check stops future discovery/evidence updates;
existing reviews, baskets, and handoff history remain available. Re-enabling a
check lets the next full refresh examine its retained evidence again. These
switches use `safety_rule_off:*` keys in the library's existing settings store.

Risky action cards are questions for the user's agent. They show a short title,
one-sentence reason, location, date, and **Add to prompt**, **Details**, and
**Dismiss**. Exact subjects, commands, investigation questions, and archived
source links live in Details. The wording never equates a selected command with
harm, authorization, or a confirmed effect.

## Handoff

The shared basket supports selecting, ordering, removing, and moving reviews
between risky action prompts. The default target is the repository, then the source
Mac, and finally the workspace when its machine is unknown. Risky action baskets do
not clone an item; one review has one current handoff.

Preview changes no state. **Copy prompt and hand off** copies the selected
reviews' investigation instructions and records their handoff. The selected
items leave the basket and Open immediately. Unticked items remain. A matching
preview token prevents recording a different selection from the one reviewed;
successful retries are idempotent. Clipboard failure records nothing. A failed
recording is shown explicitly so the user can review and retry.

The prompt asks the agent to investigate, explain, and recommend next steps.
It asks for evidence citations and benign explanations as well as concerns.
It does not authorize implementing fixes. Source commands, paths, and transcript
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

Initial rules cover credential/configuration copies, direct SQL mutations,
force pushes/ref deletion, local history changes, deletions, cloud object
deletions, merges/releases/tags, broad process selectors, external permission
changes, HTTP mutations, and system/disk-image operations. Recognized help and
dry runs do not trigger reviews. These are heuristics, not independently
validated incident findings or complete coverage of every action.

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
directories local. `cd /outside || rm -rf cache` therefore selects the original
directory; `(cd /outside && echo ok); rm -rf cache` also uses the outer directory.
When paths converge with different directories, or unsupported control flow or
shell-dependent pipeline builtins prevent a reliable inference, relative targets
remain unresolved and call-scoped. Absolute targets can still be identified.

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
`handed_off`, or `dismissed`; `safety_place` selects a stable target; `safety_case`
opens Details. Evidence and coverage disclosures use shared URL view state.
`view=table` opens its table (`q_safety`); `view=settings` opens Risky action settings.

```text
GET  /api/findings/safety
GET  /api/findings/safety/{id}
GET  /api/findings/safety/settings
POST /api/findings/safety/settings
POST /api/findings/safety/{id}/action
POST /api/findings/safety/cart/prompt
POST /api/findings/safety/cart/copy
POST /api/query/safety/rows-v2
```

MCP exposes `list_safety_cases(state?, limit?)` and `get_safety_case(id)`.
Reading changes no state. Both copy APIs reject mixing safety and efficiency
IDs. The Optimize badge includes open reviews from both sections, while each
section keeps its own counts and baskets.

The interface calls this section **Risky actions**. Existing `safety` dataset,
URL parameters, API routes, and MCP method names remain stable for saved links
and agent integrations. The sample preview shows all 13 discovery checks, with
three illustrative reviews; review count does not limit the available checks.
