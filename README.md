<p align="center">
  <img src="macos/AppIcon.svg" alt="Pharos lighthouse icon" width="160">
</p>

<h1 align="center">Pharos</h1>

<p align="center">
  <strong>A token catalog and optimizer for everything you've done with coding agents.</strong>
</p>

<p align="center">
  <a href="https://grady.dev/projects/pharos">Blog post</a> ·
  <a href="#quick-install">Quick install</a> ·
  <a href="#getting-started-from-source-code">Build from source</a> ·
  <a href="#make-it-yours">Make it yours</a> ·
  <a href="docs/technical-overview.md">Technical overview</a> ·
  <a href="#contact">Contact</a>
</p>

<p align="center">
  <img alt="macOS 14+" src="https://img.shields.io/badge/macOS-14%2B-2b5241">
  <img alt="Go 1.26" src="https://img.shields.io/badge/Go-1.26-2b5241">
  <a href="LICENSE"><img alt="PolyForm Shield License 1.0.0" src="https://img.shields.io/badge/license-PolyForm_Shield_1.0.0-d4a857"></a>
</p>

---

Pharos gathers your conversations with Claude Code, Codex, Google Antigravity
(the app and the `agy` CLI), Conductor, and ChatGPT exports into one searchable library on your Mac, or on an external drive that
moves between Macs. You can search past work by what you were trying to do, see
what each piece of work cost in tokens and dollars, find which tools and
commands your agents spend their context on, and give your agents read-only
access to the whole history over MCP. Indexing and search stay on your machine: every source
is read-only, and the service only listens on loopback.

The optional [feedback tool](docs/ui-feedback.md) sends only the report and
attachments you review and submit to Pythia Software.

For the story of why I built it and what I learned from my own numbers, read the
blog post: **[grady.dev/projects/pharos](https://grady.dev/projects/pharos)**.

![The Library tab: a search for "hardcoding" across every agent conversation, shown as a filterable table of work with repository, model, tokens, cost, and PRs](docs/images/library.png)

## Quick Install

On macOS 14 or later, you can install Pharos without building it:

1. Open **[GitHub Releases](https://github.com/Pythia-Software/pharos/releases)**,
   select the **latest release**, and download its Pharos **`.dmg`** under Assets.
2. Open the disk image and drag `Pharos.app` to Applications.
3. Open Pharos from Applications and approve the macOS access prompts. The setup
   guide asks where your library lives: **On this Mac**, or a folder on an
   attached SSD if you work across several Macs.

Pharos.app runs from Applications. A library is a separate folder, on this Mac
or on a drive. Add, open or switch libraries later in **Settings → Libraries**.
Libraries on this Mac live under `~/Library/Application Support/Pharos/Libraries/`.
See [releases and updates](docs/releases-and-updates.md) for more detail. If no
release is listed yet, use the source instructions below.

## Getting Started from Source Code

To build Pharos, you need macOS 14 or later and:

- [Go](https://go.dev/dl/). `go.mod` pins Go 1.26, and an older `go` downloads it automatically.
- Xcode Command Line Tools (`xcode-select --install`) for the Swift wrapper
- [Node.js](https://nodejs.org/) 24 LTS (pinned in `.nvmrc`) and npm to build the frontend

For a development checkout, run `tools/setup.sh` before Go builds or tests.
It installs pinned npm dependencies and generates the ignored frontend bundles.
App builds run this setup automatically. Installed release apps need no Node.js.
See [branch updates and frontend development](docs/query-table-local-development.md).

### 1. Build a library

Pharos works best as a *library*: one folder that holds the app, its
configuration, and everything it indexes. Put it on an external drive to carry
your history between Macs, or anywhere you like on a single Mac.

```sh
git clone https://github.com/Pythia-Software/pharos.git pharos
cd pharos
macos/install-library.sh /Volumes/<your-drive>/Pharos
open /Volumes/<your-drive>/Pharos/Pharos.app
```

Rerunning `install-library.sh` later upgrades the app and leaves your
configuration and catalog alone.

> Prefer a plain per-user install from source? `./launch.sh` builds and opens
> Pharos with its configuration and data in
> `~/Library/Application Support/Pharos`. In that mode you add sources to
> `archive.toml` by hand; `pharos probe` lists what it finds. See
> [Configuration](docs/configuration.md).

### 2. Choose your sources

The first time you open the library on a Mac, Pharos checks the handful of
places where Claude Code, Codex, Antigravity, and Conductor keep their conversations. It
never scans the rest of your home folder. It shows what it found: how many
sessions, how far back they go, and a warning when a tool is set to delete old
transcripts. Nothing is indexed until you pick.

![The "New Mac detected" panel listing Claude Code, Codex, and Conductor sources with session counts, sizes, and date ranges](docs/images/new-mac.png)

ChatGPT keeps conversations in the cloud, so export your data from ChatGPT and
point a source at the folder holding `conversations.json`.

### 3. Capture and index

Pharos first *captures* your conversation files by copying them, unparsed, into
the library, which takes seconds to minutes. Then it *indexes* them into a
searchable catalog. You can close the dialog and both steps keep running in the
background.

![Capture progress per source: claude captured, codex copying, conductor waiting](docs/images/capture.png)

Indexing can happen later, on any Mac. **Settings → Sources → Macs and
captures** shows every Mac in the library and what still needs indexing.

![Macs and captures: this MacBook Air's sources need indexing, while a Mac Studio's are indexed](docs/images/macs-and-captures.png)

On another Mac, plug in the drive and double-click **Add This Mac.command**
beside the app, or just open the app and accept the panel again.

### 4. Find past work

The **Library** search has three modes: conversation text, modified files, and
URLs used by tool calls. Text results group user messages, agent responses, and
retained thinking by conversation, with a link to the matching message. Put a
phrase in quotes for an exact phrase match. Fuzzy words are on by default;
case-sensitive and literal-separator matching are optional. Text search uses
the message index directly and does not rank workspaces with vectors. File
results name a conversation when the archived evidence supports that link;
otherwise they identify the workspace. URL results exclude links merely
returned by web search.

The Library table is a query builder: pick columns, filter (including OR and
NOT), sort on several keys, add metrics, and save views. A search narrows it to
the matching work, so filters and metrics combine with the search, and each
result links to its best match. Show results as a table or as conversation
summaries, with or without a search.

Open a row to see its changed files, linked PRs, and conversations. Each
conversation reads turn by turn: your prompt, the outcome, and every command,
file read, and tool call in between, marked with how many tokens it added to the
context. The rail on the right tracks context size and turns across the whole
conversation, and you can click it to jump to any point.

Work names use recorded source titles, names from declared session identities,
or a cached excerpt of the first words classified as human-authored. See
[Conversation and work names](docs/conversation-naming.md) for naming and capture rules.

![A conversation in Pharos: turns with prompt and outcome previews, an activity timeline of commands and reads with context-growth markers, and a context minimap on the right](docs/images/conversation.png)

**Share** saves conversations as one standalone HTML file you can send or
publish. On a work's page it shares that conversation; in the Library, tick
the rows you want (or **Select all** for the current filters) and choose
**Share N conversations**, up to 200 at a time. The file opens in any browser
with the same reader (search, turn disclosure, tool details, the context rail,
the token overview) and tables over those works' Library rows, tool use, tool
calls, and token usage that filter, group, and chart in the page. It needs no
Pharos, network, or server, and ends with a link back to this repository. It
contains the full transcripts, tool output, and file paths, so read it before
sharing.

### 5. See where the tokens go

**Analyze → Machine Activity** has three views: **Machine Tokens**, **Machine
Parallelism**, and **Machine Time**, with shared period, repository, provider,
and model filters. Machine Tokens breaks down tokens and API-equivalent cost by day,
model, effort level, provider, repository, and agent. Costs use list prices on the day of use,
so treat them as a lower bound.

Machine Time compares total duration, call volume, and time per timed run by
repository, activity, parsed command family, or specific tool. Named scripts
and server-qualified MCP tools keep separate identities. Chart selections and
the native raw-call query table share filters, so full invocations and source
conversations remain inspectable. Previous-period averages use total duration
divided by timed calls; missing durations stay separate. It reports tool time,
including overlapping calls, rather than
total agent compute or elapsed work time. Machine Parallelism uses the existing
captured timing snapshot when available. Both views have an explicitly labeled
**Example data** option for exploring the design without a populated library.

![The Analyze tab: tokens per day split by model, with total cost, cost per month, and cost by provider](docs/images/usage.png)

**Analyze → Human Words** estimates how much you actually typed, as opposed to
pasted, re-sent, or harness-injected text.

![Human Words: words typed, words per message, and a breakdown of where user-turn text came from](docs/images/human-words.png)

**Analyze → Carbon Impact** estimates the electricity and CO₂e behind your token
usage. Compare all time with the last 30 days and adjust the grid, data center
overhead, and energy assumptions.

**Tools** joins every tool call to its result. It shows which calls fail, which
are slow, and which results bloat the context window for the rest of the session.

![The Tools tab: every tool call with its command, status, duration, and context cost, plus a calls-by-tool chart](docs/images/tools.png)

### 6. Fix what keeps going wrong

**Findings** turns the largest recurring, fixable patterns into prompts for an
agent: failures agents keep hitting, instructions one harness can't find, tools
they keep looking up, commands that flood the context. Collect findings into a
prompt for a repository, copy it, and Pharos measures whether the problem went
away, with a running tally of what the fixes saved. See [Findings](docs/findings.md).

### 7. Connect your agents (optional)

The **MCP** tab gives you a copyable stdio configuration for your agent client.
Your agents can then search and read past conversations and findings. The
tools are read-only, and one switch turns them off for every client.
Agents can also filter and sort by usage metrics, retrieve compact IDs and
statistics, or ask for counts and averages through `query_statistics`.
See [MCP statistics](docs/mcp-statistics.md) for scopes, quality, and paging.

### 8. Unplug safely

The drive badge in the header shows what Pharos is doing with the library right
now, with measured stage progress and time ranges based on workload size and
recent processing speed. The panel explains when the remaining work is still
unknown; see [progress and estimates](docs/progress-estimates.md). Use
**Eject** in its panel instead of pulling the drive: Pharos stops its own work
at a safe point, closes the library, stops agents' Pharos MCP servers running
from the drive, quits, and ejects it.

<p align="center">
  <img src="docs/images/drive-panel.png" alt="The library drive panel" width="480">
</p>

For everything else (the CLI, the HTTP API, configuration keys, code signing,
backups, and development), see the [technical overview](docs/technical-overview.md)
and [configuration reference](docs/configuration.md).

### Keep Pharos in the Mac menu bar

The Mac app shows a still, monochrome lighthouse with a separate status dot:
green when idle, yellow while working, and red when Pharos needs attention.
It keeps updating with the main window closed. Click it to reopen Pharos or
see rolling 24-hour words typed, tokens used, and estimated carbon impact.

**Settings → App preferences → macOS menu bar** turns the icon on or off and
chooses which menu lines appear. Their order is fixed. Last-index freshness
and the three usage metrics are on by default; estimated API cost, last
successful backup, library size and free space, Index changes now, and safe
Eject are optional. Eject appears only for an ejectable library drive.
Carbon uses the assumptions in Analyze → Carbon Impact; API cost is the same
list-price estimate used in Analyze. These preferences follow the library
between Macs.

## Make it yours

Pharos is built around one person's workflow: the agents I use, the questions
I ask about my own work, and the way I move between Macs. Your setup is
different, so **please fork it for personal or internal company use** and bend
it to fit, under the [license](#license). Contributions are welcome. Some starting
points:

- **Add a source.** Adapters live in `internal/archive/adapters.go`,
  `adapter_conductor.go`, and `adapter_exports.go`, and the places Pharos looks
  on a new Mac are in `internal/archive/probe.go`. The [canonical export
  format](docs/canonical-export.md) is the easiest way to bring in anything else.
- **Ask different questions.** The queryable fields for every table are defined
  in [`schemas/`](schemas). The same files drive the React UI in `web/src` and
  the Go query executor.
- **Fix the prices.** Model prices live in `pricing/cost_changes.json`. See
  [`pricing/README.md`](pricing/README.md).

### A note on TL1

You'll find code here that refers to a project called **TL1**: the `tl1` and
`tl1-export` source kinds, `internal/archive/tl1_*.go`, and the mothballed
workspace-reclamation contract in [`docs/tl1-contract.md`](docs/tl1-contract.md).
TL1 is a private agent orchestration tool I use, and it isn't publicly
available. That code is customized for my use cases. Pharos works without it.
Dedicated TL1 analysis pages are removed for now; ingestion, archived
conversations, findings, APIs, MCP tools, and the attempts dataset remain
available. Attempts can be queried through Analyze's shared query table.

## Contact

Found a bug, or have an idea for an addition? **[Open an issue](https://github.com/Pythia-Software/pharos/issues)**.
I'm happy to discuss new sources, new analyses, or anything you've built on a
fork. You can also find me at [grady.dev](https://grady.dev).

## License

[PolyForm Shield License 1.0.0](LICENSE) © 2026 Grady Berry Ward

Pharos is source-available. Personal use and internal company use are permitted,
including using and modifying Pharos to analyze and optimize AI usage and
spending at work. Contributions are welcome.

You may not use Pharos or modified versions of it to provide a competing product
or service, including a hosted Pharos service for customers or a rebranded
competing fork, whether paid or free. This is a summary; the full [license](LICENSE)
governs.

Earlier versions distributed under the MIT license retain their MIT license
grants; this change does not revoke those permissions. Third-party components
retain their own licenses and notices, including the separate
[Query Table MIT license](internal/querytable/LICENSE).
