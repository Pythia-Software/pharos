# Fixes in Optimize

**Fixes** lists the problems agents keep running into that you could prevent once,
with a change to guidance, the environment, a tool, the setup or the docs. Examples
are a missing or wrong-version tool, a shell or sandbox surprise, an integration
that keeps failing, a check that is known to fail, an out-of-date doc, or a fact
about the code that agents keep working out again. It sits in Optimize beside
**Efficiency** and **Risky actions**, and works the way Risky actions does:

- items move between Open, Handed off and Closed;
- there is one prompt basket per place;
- copying a prompt records a handoff;
- a problem that recurs after its handoff opens again.

Like Risky actions, Fixes has three pages in the Optimize menu:

- **Dashboard**: the cards, by state, place and kind;
- **Table**: every item in a Query Table (`fixes` dataset), hidden ones included,
  with an **On the menu** column saying whether Open lists it and, if not, why;
- **Settings**: the bounds below.

Fixes is built from [agent events](agent-events.md), which are still a preview, but
the menu lists it either way: with no events it says so.

## Grouping (background)

`refreshFixes` (`fixes_store.go`) runs at three points:

- after the agent events pass, at most hourly (`fixesChainInterval`);
- at the end of the events backlog;
- in the full Findings pass, after risky actions.

Opening the page never starts it. A fingerprint skips the run when nothing it reads
has changed: event count, latest event, messages read, latest day and today's date.

The grouping in `fixes.go` and `fixes_grouping.go` is a port of the Agent Experience
lab's "v3" grouping:

1. **Leave out request events first.** Events about the person's request are
   dropped: unclear instructions, disagreeing, and assumptions, confusion or
   mistakes that refer to the request.
2. **Group similar sentences.** The remaining sentences are tokenized with the
   event lexicon's cue words removed and paraphrases folded. They are clustered by
   TF-IDF similarity:
   - a leader join needs two shared concepts, one of them a key term;
   - similar groups then merge by centroid;
   - this runs once per pass: friction, learning, confusion and stale guidance.
3. **Keep groups that recur.** A group must span at least two work items and be
   cohesive.
4. **Merge groups into items.** Groups that share a key term, a kind of fix and a
   destination become one item.
5. **Add stale-doc items.** Stale-guidance events that name the same doc in the
   same repository become an item of their own.
6. **Assign a scope.** An item is **global** when it spans two or more repositories
   and none holds 85% of its events; otherwise it belongs to its repository.
   Repository sharing decides where the prompt goes and is never an input to
   grouping.
7. **Title it.** An item's title names the problem from its key term, kind and
   place, for example "`trimesh` missing from corpus's environment", "`gh pr edit`
   keeps failing for agents" or "Known failing check: …". The item's best
   self-contained member sentence (first person, plans and back-references ranked
   down) is kept as its **lead** and quoted under the title.
8. **Merge known failing checks.** A check that fails regardless of the change
   (fails on master, unrelated to this change) is a known failing check, not an
   environment problem. Items in the same place that name the same check (a quoted
   name, or two shared distinctive words such as "dashboard parity") merge into one.
9. **Mark intended constraints.** When at least two events, and a fifth of the
   item's, cite a rule they are following ("per CLAUDE.md", "the guidance says
   never…"), the item is marked **Guidance already covers this (intended)** and left
   off Open whatever the bounds. Guides read include the repository's CLAUDE.md and
   AGENTS.md and the Claude memory files for its clone.
10. **Assign a status.**
   - *Still recurring* means it was seen in the last three days the pass read.
   - *Quiet* means its expected count since then is at least three, but none
     appeared.
   - *Quiet since a guidance commit* means a commit since then names one of its key
     terms.
   - Anything else is *unclear*.

An event with no time of its own (some imported Codex replies) takes its message's
time, or else its conversation's start.

Item IDs are stable, made from the kind of fix, the target and the anchor term. This
keeps triage, baskets and handoffs across rebuilds. Items are stored in `fix_items`.
The person's choices are stored in `fix_state`, `fix_cart` and `fix_handoffs`.

## Bounds (settings)

Three library settings, stored in `finding_settings`, keep the menu useful:

| Setting | Default | Effect |
|---|---|---|
| Rank lower once quiet for (`fixes_quiet_days`) | 7 days | Priority × 0.6 past it, × 0.3 past three times it |
| Hide once quiet for (`fixes_hide_days`) | 30 days (0 = never) | Off Open until it recurs |
| Hide when seen in fewer than (`fixes_min_work_items`) | 3 work items | Off Open |

"Quiet" counts days before the latest day the events pass read, not before today. A
library that has not been read for a while therefore does not hide everything.

Priority is work items × the quiet weight, plus 2 × the hours of friction episodes
the problem started or reported (at most 20 hours). An episode reported in a reply
after the fact runs from the turn's first failed call to the report. Harnesses that
record no call times (TL1 Codex) have no measurable time lost. It is computed when the page is read, so a
change to a bound applies at once.

The bounds apply only to Open. Handed-off and closed items always stay listed. The
dashboard says how many items the bounds hide, `fixes_hidden=1` shows them,
marked, and a link opens the Table filtered to them. The settings page shows how
many items each option would hide.

## Boundary with Risky actions

An agent getting around a safeguard (`circumventing_constraint` events) is a
**Risky action**, not a fix.

- **Why.** One instance can matter. These events should be reviewed one at a time,
  never grouped or hidden for being rare.
- **Where they go.** `safety_safeguards.go` turns each such event into a notice under
  one of four checks. Like the other checks, each can be switched off in Risky action
  settings.

  | Check | Danger |
  |---|---|
  | `safeguard-test`: weakened or skipped a test | medium |
  | `safeguard-restriction`: worked around a harness restriction | medium |
  | `safeguard-gate`: bypassed a product gate | medium |
  | `safeguard-other` | low |

  The notice's evidence is the agent's own sentence, with a reader link.
- **What Fixes does with them.** Fixes leaves these events out. An item still counts
  the safeguard events in its conversations and links to Risky actions, so the two
  views point at each other without showing the same thing twice.

The two areas share their frontend parts:

- the card (`OptimizeCard`), prompt basket (`OptimizeCart`) and selection bar
  (`OptimizeSelectionBar`), in `optimize-shared.tsx`;
- the selection plan (`optimizeSelectionPlan`, in `optimize-model.ts`);
- the prompt review dialog (`ReviewDialog`, with `area="fixes"`).

Their baskets stay separate.

## Prompts

A prompt goes to one place: the shared setup (global guidance, the environment and
shared tools), or one repository. It holds at most 12 items. It is a template filled
from the evidence: verbatim quotes, exact counts, named files, guidance matches and
status. No language model writes it.

The review dialog asks for one of two modes:

- **Investigate and propose** changes nothing and reports the proposed change, with
  exact wording for any note.
- **Make the change** confirms each problem first, then makes the smallest change
  that stops it, on a branch.

## API, MCP and links

**HTTP.** All endpoints are under `/api/findings/fixes`:

| Method | Path | What it does |
|---|---|---|
| GET | `/api/findings/fixes` | The overview |
| GET | `/{id}` | One item's detail |
| GET, POST | `/settings` | Read or save the bounds |
| POST | `/{id}/action` | Act on one item |
| POST | `/bulk` | Act on many items |
| POST | `/cart/prompt` | Preview a prompt, with a preview token |
| POST | `/cart/copy` | Record the handoff |

The actions are `dismiss`, `restore`, `cart_add`, `cart_remove` and `cart_tick`.

**MCP.**
- `list_fixes` takes state, category, target, include_hidden and limit.
- `get_fix` returns one item with its quotes.

**Links.** Fixes lives at `/findings?optimize_area=fixes`. Its view fields (see
`view-contract.ts`) are:
- `fixes_list`, `fixes_place`, `fixes_kind`, `fixes_rank`, `fixes_hidden` and
  `fixes_item`;
- `view=table` for the table, whose query is `q_fixes`, and `view=settings` for the
  settings page;
- `fix:<id>` as a reveal target.
