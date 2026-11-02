# Incremental update cost

Measured overnight on November 1–2, 2026. Goals: a scheduled update that finds
nothing new finishes in under 10 seconds, and 1,000 new messages index and
analyse in under a minute. Cloud work (GitHub, local and cloud backups) is out
of scope; every other update step is in.

## Workload and measurement

The replica is an APFS clone of the November 1, 10 PM backup of the
production library: a 110 GB catalog with about 5.4 million messages. A
script rewrote this Mac's source paths to point at a cloned snapshot of the
real provider sources (Claude, Codex, Conductor, Antigravity), and dropped and
recreated the triggers it touched. The production catalog, service and sources
were not read under a transaction or modified. The host is an Apple M3 Ultra
with 96 GiB RAM; other jobs shared it throughout.

Each run starts the service on a fresh clone of the same catalog, with
automatic updates off, and asks it to run one background item at a time. The
standard sequence is:

1. two no-op indexes against source snapshot S1 (the first after startup is
   cold), then Repositories, Tools, Writing (authorship) and Findings;
2. the sources switch to snapshot S2, taken about an hour later: 21 changed
   conversations, 6,500 messages in the changed groups;
3. the same items again.

Wall time and step names come from the operation journal and `sync_history`;
CPU and peak RSS sample the service process every half second. These are
single observations on a shared machine, not repeated estimates; differences
under about 20% are noise.

Page-cache state dominates the delta index. A freshly cloned catalog is cold.
In these measurements, a run that started on a later day than its catalog's
tool cube first rebuilt the cube (see Day rollover below), reading every tool
call, and its delta index then ran about 20 seconds faster than the same
build's runs on the earlier day. The `claude` phase fell from about 26 to 6
seconds. Sampled Go CPU was about the same in
both; the extra time fell outside it, which is consistent with page reads.
Only runs on the same side of that boundary are compared below.

The authorship state version changed in this work, so its runs start from a
catalog that has already completed one full pass at the new version.

## Results

Before and after, S1 → S2, both runs on November 2:

| Step | Before | After |
| --- | ---: | ---: |
| Scheduled no-op index, warm | 14.6 s | 4.3 s |
| Identities, no-op | 9.0 s | 0.6 s |
| Delta index | 40.4 s | 27.7 s |
| Repositories (identities and Git), no-op | 11.0 s | 2.8 s |
| Delta Tools | about 12 s ¹ | 0.5 s |
| Delta Writing (authorship) | 209.2 s, full pass, 6.6 GB peak RSS | 0.8 s, suffix, 3.9 GB |
| Delta Findings | 10.8 s | 2.7 s |
| Delta, all steps | about 285 s ² | 34.2 s |

¹ The original build does not journal short operations; the Tools item used
11.6 CPU-s, and the November 1 baseline journaled it at 12.9 s. ² Taking
Repositories at its no-op time.

The first index after the service starts took 30.9 s before and 17.1 s after
the run-log cache (November 1). It reads Conductor's database and the catalog
cold.

A larger delta, from S1 to a snapshot taken five hours later (S3), adds about
5,600 messages and 1,250 tool calls to the same 21 conversations (11,800
messages in the changed groups). Both runs started on a later calendar day than
their catalogs' last Findings pass, so Findings also rolled over its window:

| Step, S1 → S3 | Before | After |
| --- | ---: | ---: |
| Delta index | 54.9 s | 40.8 s |
| Identities | 8.3 s | 0.6 s |
| Tools | not journaled | 0.4 s |
| Writing (authorship) | 222.7 s, full pass | 0.9 s, suffix |
| Findings (new day) | 30.5 s | 28.7 s |
| Next scheduled no-op | 23.8 s | 5.0 s |

The delta index's gain is mostly discovery (18.2 → 5.9 s); the Claude and
Conductor write phases are unchanged at about 16 and 12 seconds.

The scheduled no-op skips Tools, Writing and Findings, which run only when
their inputs changed. About 3 of the warm no-op's 4.3 seconds is the one
audit each index run performs.

## Changes, in order

Each row is the standard S1 → S2 sequence with that change and all earlier
ones. The last two rows ran on November 2, the rest on November 1; the delta
index is comparable only within a day.

| Change | No-op warm | No-op cold | Identities | Delta index | Delta tools | Delta authorship |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Baseline (master 4b6ea1d4) | 23.0 s | 30.9 s | 10.5 s | 59.5 s | 12.9 s | 232.7 s |
| Antigravity run-log cache; tool-command upserts | 4.9 s | 17.1 s | 8.4 s | 47.6 s | 12.8 s | — ³ |
| Identities; authorship for new work | 5.1 s | 17.8 s | 0.7 s | 53.1 s | 13.1 s | 14.8 s ⁴ |
| Stored-launch reuse | 5–6 s | 16–18 s | 0.6 s | 44–54 s | 11–16 s | 0.8–1.5 s |
| *November 2:* baseline again | 14.6 s | — | 9.0 s | 40.4 s | about 12 s | 209.2 s |
| Audit ranking; token decode; changed-value dirtiness | 4.3 s | — | 0.6 s | 27.7 s | 0.5 s | 0.8 s |

³ Not separately recorded. ⁴ Derived launches on every suffix pass, before
reusing stored ones. The November 2 first index ran beside the cube rebuild,
so its cold time is omitted.

**Antigravity run logs.** The run-log cache held 4,096 entries for a
directory of about 15,000 logs, so each index re-read and regex-scanned every
log: about 13 CPU-s of the 23-second no-op. The cache now keys each log's
conversation IDs by path, size and modification time without a cap, prunes
entries whose files are gone, serves the audit and capture paths too, and
finds IDs with a scanner rather than a regular expression.

**Tool commands and URLs.** Re-indexing a call deleted and re-inserted its
`tool_commands` and `tool_urls` rows. The delete dated authorship's dirty
evidence back to the call's start, which in this delta forced the full
authorship pass (233 s, 6.4 GB RSS). Rows now upsert only when they change,
and positions that disappeared are deleted.

**Identities.** Reconciliation re-derived aliases for every conversation and
names for every workspace. It now handles only undeclared conversations and
the workspaces whose links or activity changed.

**Authorship for new work.** The checkpoint's graph fingerprint covered every
conversation, so any new conversation, workspace or headless run forced the
full fallback. It now covers the conversations that started by the watermark
(see [Human authorship](../human-authorship.md)). A new conversation dates its
dirty evidence by its start rather than by when it was indexed. When a new run
arrives, the suffix pass derives launches and keeps the suffix path only if
every stored launch is unchanged. Otherwise it reuses the stored launches
without deriving them.

**Audit ranking.** Every discovered unit re-sorted the 64-candidate audit
pool, building two key strings per comparison: about 1.5 GB allocated per
index. Each unit's rank is now read once, when it is considered.

**Token decode.** Both passes over a conversation's token report decoded
every message's JSON again. Each is now decoded once per report.

**Tool rollup dirtiness.** Every index run upserted all repositories, and the
workspace, session and repository update triggers marked a rollup partition
dirty for any update. One delta dirtied 11,997 workspaces (237,000 calls).
The upsert now skips unchanged rows, and the triggers fire only when a watched
column's value changes.

**Day rollover.** The tool cube was rebuilt in full on the first rollup of
each day: about 80 seconds, 30 of them holding the writer, and 60 CPU-s on
this catalog. Nothing in it depends on today: it groups each call by its own
local day, and a timezone change still forces the rebuild. A new day now
leaves it current: starting the service on the previous day's replica took
29.3 seconds and 9 CPU-s (1.9 GB peak RSS), against 113.9 seconds and 70
CPU-s (3.8 GB) before. The date is still recorded, because earlier builds
that open the same library rebuild when it is not today.

## What remains expensive

- **Writing changed conversations.** In the S1 → S3 delta the Claude and
  Conductor phases took about 16 and 12 seconds for 5,600 new messages, with
  a cold page cache costing up to 20 seconds more.
- **Origins.** `publishOrigins` recomputes every origin after any index that
  wrote: about 1 GB allocated and 3.5 CPU-s.
- **First index after start.** Discovery reads Conductor's database and the
  catalog cold: about 16 seconds wall for 8 CPU-s on this external drive.
- **Findings refresh** loads its whole window: about 11 seconds when run
  without changes, 29–30 seconds on a new day's first pass, and 3 seconds
  after the S1 → S2 delta on the same day.
- **Upgrading authorship state** runs one full pass, about four minutes and
  6 GB RSS on this catalog.

## Replica caveats

The replica's `messages.evidence_locator` keeps the original Codex paths, so
the audit of one Codex rollout reports a mismatch that production would not.
Replacing the source snapshot changes inodes, which Antigravity CLI's
request-database signal includes, so the first index after a swap re-plans
its 500 groups.
