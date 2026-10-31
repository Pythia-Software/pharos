# Ask Pharos requirements

Ask Pharos lets users ask questions about their past work and navigate to relevant
Pharos views. The current interactive design review lives in
`.context/ask-pharos-sketch.html`. These requirements describe the full build;
the sketch uses simulated responses.

## CLI access and existing search

Use an installed `codex`, `claude`, or `agy` CLI after explicit opt-in. The initial
build does not accept AI provider API credentials. Preserve existing search when
no supported CLI is available, when the user opts out, and through the mode switch
inside the AI composer. Users can select the CLI, model, and effort level and save
a default selection. Production choices must reflect the available CLI's actual
capabilities rather than the sketch's example models.
Use a compact star in the model picker to save that default.

Without a saved selection, prefer GPT 6.1 Sol with Low effort for Codex,
Gemini 3.8 Flash with Default effort for Antigravity, and Sonnet 5.5 with Medium
effort for Claude. Only select models and effort levels offered by the installed
CLI; fall back to its advertised default model when the preferred model is
unavailable, and Default effort when the preferred effort is unavailable.
Preserve explicit saved selections and existing chats.

The “Try Ask Pharos” invitation opens a modal explaining plain-language navigation,
the installed and authenticated CLI requirement, account quota consumption, and
that these conversations will themselves be indexed by Pharos. Explain that the
feature is optional, no other use of Pharos involves AI, and disabling it means
Pharos never accesses agentic CLIs.

Offer three choices: “Enable Ask Pharos”, “Not right now”, and “No, and don't show
this again”. Not right now dismisses the current invitation without permanent
suppression. The final choice disables the feature and persistently hides AI
invitations. Settings must expose both feature enablement and invitation visibility
so users can reverse this choice. Enabling from Settings still presents the opt-in
explanation. Disabling must stop active requests and navigation and preserve
ordinary search.

Once the first question is sent, lock the CLI, model, and effort for that
conversation. Starting a fresh chat with Clear unlocks the selector; reopening an
older chat restores its selection and keeps it locked.

The active chat has an `ask_chat` ID in the landing-page URL, including drafts.
Opening that URL restores the chat independently of this tab's remembered session.
History entries are links that can be copied or opened in another tab. Opening
history or clearing the current chat supports browser Back/Forward navigation.
Reloading restores this tab's open overlay or floating progress panel, including
the draft and page context. Closed or dismissed surfaces stay closed; restoring
an overlay preserves the underlying page URI.

## Composer and responses

The prompt editor expands automatically. Focus enlivens the whole composer and its SVG
ribbon border. Provide light and dark appearances, a paper-plane send button, and
an icon beside it to switch to ordinary search.

The expanded composer, floating progress panel and header button share the
`createPharosGlow` effect in `assets/ask-glow.js` and the `.pharos-glow` styles.
The effect owns its SVG contour, ribbon, aura, resizing and working state; attach
it to another surface, call `setWorking` with the active request state, and
`destroy` it when that surface is removed. Hue cycles slowly while idle and six
times faster while working, preserving its current phase when speed changes.
Only the decoration changes hue; content and panel fills retain their colors.
Reduced motion keeps both hue and loading trace still, with loading still visible.
For an icon, supply its 24×24 SVG outline path to the same effect. The header's
chat glyph uses this mode so its glow and loading trace follow the bubble inside
the button, leaving the button's ordinary border and background intact.

Submitting creates a conversation inside the composer: the user's question,
processing state, and agent reply all remain in the same thread. Follow-ups append
to that thread. Keep each reply's original CLI, model, and effort identity. The
Pharos lighthouse avatar has a rounded-square outline and an overlapping provider
badge at its bottom-right corner. Use a human icon for the user in the same size
and shape of frame. Omit visible speaker labels ("You" and the agent name).
Give the user's message a soft, theme-aware background with rounded corners
and no border; keep the agent reply on the composer surface. Keep accessible
message-group labels. Show configuration in the composer selector rather than
repeating the model and effort alongside each agent reply. Keep ribbon corners
smooth and round as the conversation grows.

Keep long conversations in a scrollable area above the editor. Size it around
the tail of the preceding agent reply, the latest user prompt, and the latest
agent reply, capped by the available window height. All older messages remain
accessible by scrolling. Opening a chat and sending a prompt focus the newest
exchange; updates follow the bottom only while the user is already there. Reading
earlier messages preserves the scroll position and offers a “Latest reply” button.

Responses can contain one or several destination cards with descriptive titles
and explanations. A response with exactly one destination that has no chart
preview opens it immediately, including when the user phrases the request as a
question. Multiple matches remain
choices, and so does a lone card left after Pharos dropped others the agent
offered beside it. Typing a follow-up while waiting or changing to ordinary search suppresses
automatic navigation.

Supported Analyze dashboard destinations display a compact, read-only chart above
the destination link, using the same URL-based preview loader as Home dashboard
cards. Chart answers stay in the thread, including a single result; ordinary
single-link answers retain automatic navigation. Previews load when visible,
show their time range and update time, and retain the link for empty or unavailable results.
Reopening a chat loads the addressed moving view rather than freezing its data.
Retained previews also revalidate when Ask reopens and on dashboard calendar or
dataset refreshes; hidden charts defer their queries until visible again.
Preview loading must not change the surrounding page URL, preferences or focus.

Validate destinations through Pharos's own routing and URL builders. If every
returned destination fails validation, give the agent one
automatic correction attempt with its previous response and the validation errors,
within the original request deadline. Keep private lookup results and their
remaining budget; correction must not repeat those lookups. If correction also
fails, show the validation reason without dumping the URL. A response with some
valid destinations keeps the existing choice behavior without a correction call.

Ask Pharos is also a configurable, icon-only header tool, with a chat glyph
and ribbon edge. It opens the same composer in a modal over the current page,
without an outer card. The empty modal starts a quarter down the viewport. As the
conversation grows, it expands equally above and below its original center until
it reaches a viewport edge, then uses the remaining space, keeping the
editor and controls visible while the transcript scrolls. Opening, height changes,
and repositioning animate gently; reduced motion disables those animations.
A dismissible context chip is an atomic inline part of
the prompt, alongside ordinary text. Drag it to a caret position within the
prompt, or focus it and use Alt+Left/Right to move it with the keyboard. Backspace
or Delete removes a focused chip; undo and redo restore text edits and chip
movement or removal. The editor supports multiple chips without merging their
metadata into the question text. The page chip attaches that page's local URI,
including its filters and view settings, to the request.
Reloading the same chat in the same tab preserves chip placement with the draft;
stale positions never replace a newer text draft.
Removing the chip preserves the draft and omits that URI from subsequent requests
in the open modal. Opening the modal again attaches the current page. Retries
retain the original request's URI. This context stays out of the indexed
conversation text.

A picture-in-picture icon moves the chat from either Home or the overlay into
a draggable, dismissible progress panel. Keep the icon in the composer footer,
immediately before Send/Stop. While processing, show “Browse while you wait”
below the icon with a small gap. Clear the hint when processing ends or browsing
is chosen, including when reopening that running request. Drag the panel itself to move it, or
tap it once to reopen the chat; drafts and active questions stay intact.
The panel shows only the latest streamed activity or request state
and announces a completed response, clarification, or error. Dismissing it keeps
the request running; the header button marks a ready response and reopens the
chat. Once browsing has been chosen, the request never automatically navigates,
even if the chat is reopened before completion. Destination cards remain
explicit links. Closing a pending modal also switches to browsing.

Loading needs elapsed time, cancellation, retry, and preserved drafts. Suggestions
prefill the editor without submitting and follow the existing card carousel's
interaction patterns.

The suggestion catalog in `assets/ask-model.js` interleaves questions about
instructions, failures, tools, context, usage, models, delegation, prior work,
findings and activity. Optional repository phrases use names from the Library's
distinct repository values, and appear on only some eligible questions when
at least two named repositories exist. Comparisons across repositories remain
unqualified. Refresh changes both the page of ideas and the repository examples;
missing metadata leaves the broad questions available. These are invitations to
investigate, not claims that a pattern or a finding exists in the library.

Hide/Show suggestions remains above the cards. Its collapsed state is stored in
the library's UI preferences (`pharos-ask-suggestions-hidden`), survives reloads
and follows the library between Macs. Collapsing also hides the carousel controls
and pauses rotation. It does not affect the composer or the saved chat.

When an ambiguity would materially change the destination, the agent can return
a structured `clarification` containing one question and one to three suggested
answers (usually two). This is mutually exclusive with destinations and private
lookups. Pharos renders lettered answer buttons and adds an Other text field.
Selecting a suggestion submits it immediately; Other supports Enter to submit and
Shift+Enter for a line break. The ordinary composer remains available for any
free-form reply. Answering preserves any unrelated composer draft.

A clarification finishes the CLI request and waits without a running agent.
The question and options are persisted and indexed with the visible conversation;
the next agent request receives them alongside the user's answer. Unsubmitted
Other text survives reloads. Only the latest question's controls accept answers;
older questions remain visible in the transcript. The agent should ask only when
needed, then proceed once the ambiguity has been resolved.

Each suggestion card has a smooth ribbon on one corner or two opposite corners;
never decorate adjacent corners. Vary the curvature and color orientation between
ribbons. Keep these choices stable during resizing, theme changes, and carousel
movement, and capture them in design-review snapshots for accurate replay.

## Private page visit history

An agent launched by Ask Pharos must be able to consult the user's Pharos page
visit history. Support requests such as:

- "Show me new pages I haven't seen before."
- "What was the page I was on yesterday looking at X?"

Record enough context to identify and reopen the actual view, including its route,
relevant query or filter state, title or description, and visit time. Interpret
relative dates in the user's timezone. Distinguish an unvisited view from one for
which visit history is unavailable; meaningful query state matters when identifying
what the user saw.

This history must not be exposed through the general Pharos MCP service, its tools,
or general search responses. Provide a separate private interface to agents started
by Ask Pharos, for example a custom local CLI or a dedicated connector. Scope its
authorization to the launched agent/session; general MCP credentials must not grant
access. The precise transport, storage, and retention policy remain implementation
decisions.

## Ask Pharos conversation history

Persist conversations initiated through Ask Pharos so users can find, reopen, and
continue an earlier chat. Preserve questions, agent replies and destination cards,
the identity used for each reply, and relevant timestamps. Give sessions stable
identities so navigation can return to a particular chat. Keep this history
distinct as a session history from the archived coding-agent conversations Pharos
already indexes. Ask Pharos conversations are also indexed, as disclosed at opt-in.

Ask Pharos should be able to find earlier sessions and return links to them when
the user asks.

Provide an icon-only History button with a list of sessions identified by their
first question, with recency and enough context to distinguish similar chats. Reopening restores
the conversation and draft. A Clear button stops pending work and navigation,
clears the active composer, and starts a fresh session. The previous session remains
accessible in History; Clear does not delete saved chats. Ordinary search remains
available. Clear keeps the selected CLI/model/effort for the next chat.

Place the icon buttons inside the composer in this order: switch to ordinary
search, History, Clear, picture in picture, Send/Stop. Show History only for an empty new composer, with no
submitted messages or draft. Show Clear whenever the composer has a draft or a
conversation, as a text button floating in the composer's upper-right corner.
In the modal, place a matching Close text button beside Clear. Their transparent
container takes no height in the prompt layout. Text wraps around the controls
on its first line; context chips flow inline with it. Use a transparent background
and border, and the short tooltip “Clear chat and start fresh”. Keep the controls
outside the scrollable transcript so they remain reachable while reading earlier
replies.
Disable and grey out Send when the draft is empty or contains only whitespace,
or while a conversation is loading. During a request, replace Send with an enabled
square Stop button that cancels the request while preserving any follow-up draft.

For a failed or canceled question, offer “Try Again with a different model”
alongside retry, ordinary search, and Clear. Clear uses the same fresh-chat behavior
as the composer’s Clear button. Retrying with a different model starts a fresh chat
with the full conversation in the composer: user messages, Pharos replies,
clarification questions and choices,
and result links, followed by any unsent composer draft. Label speakers and failed
or canceled replies. Open the model picker so the user can choose a CLI, model, or
effort before sending. Fit the restored text within the server’s 12,000-byte UTF-8
prompt and draft limit, including speaker labels and omission notices. Prioritize
the unsent draft and the latest complete turns, shorten an oversized latest turn
or draft without splitting Unicode code points, and label omitted or shortened
content in the composer. Keep the original chat in History.

## Design review behavior

The prototype provides the History and Clear flows with browser-local storage.
Annotation snapshots include the session list, selected session, menu visibility,
draft, opt-in modal, and access preferences so feedback can reproduce those states.
Replaying a captured state must not overwrite the reviewer's saved chat history or
access preferences. Private visit-history access and
real CLI execution belong to the full build.

## Initial implementation

The home page now mounts the approved composer using `ask-pharos.html`,
`ask-pharos.css`, and `ask-pharos.js` under `internal/archive/assets/`. The
existing React `HomeSearch` is retained inside it. Interaction rules which do
not need the DOM live in `ask-model.js`. `/settings/ask` provides opt-in,
invitation visibility, and default CLI/model/effort selection.

The Go service owns CLI discovery, consent enforcement, invocation, cancellation,
chat storage, and destination validation. Detecting an executable checks PATH
and common macOS installation directories; it never executes the CLI. After
opt-in, model discovery uses Codex's app-server `model/list`, Claude's SDK
initialization response, or `agy models`. Actual model/effort values come from
those responses. Antigravity's advertised model variants become model and effort
choices. Model discovery makes no inference request. It runs in the
background: enabling Ask Pharos, or starting the service with it enabled,
discovers every installed CLI's models, and the picker answers from that
in-memory list, refreshing one over an hour old behind the answer. Only a CLI
with no list yet waits for discovery. A failed refresh keeps the previous
list. Discovery errors preserve ordinary search and explain that the CLI may
need updating or authentication.

Each question uses noninteractive structured output, a disposable working
directory, bounded output and a three-minute deadline. Browsing the site map
adds tool calls inside that run; the motivating Opus-tokens question took
12–22 s end to end on Claude and Codex (2026-10-28), so the deadline stays. Cancellation stops the
process group. Codex runs ephemeral with read-only sandboxing and shell tools
disabled; Claude runs without native tools, hooks, session persistence, or
inherited MCP configuration; Antigravity uses a dedicated navigation agent,
plan mode and its terminal sandbox. No adapter uses permission-bypass flags.
Provider login remains the CLI's responsibility; Pharos accepts no API keys.

Every inference launch explicitly connects the read-only Pharos MCP server for
the current library using the same executable and configuration as Settings →
MCP, including portable libraries. Codex requires that server to initialize and preapproves only its tools,
which `approval_policy="never"` would otherwise refuse;
Claude loads only that server and preapproves its tools; Antigravity configures
it as `pharos-ask` in the disposable workspace and enables `call_mcp_tool`.
The distinct name avoids reusing a global `pharos` connection to another library.
Connection settings are scoped to the disposable invocation and do not edit the
user's CLI configuration or pass the Pharos API token to the agent. MCP must be
enabled in Pharos before the agent starts. Model discovery does not need this
connection.

Antigravity has no per-invocation permission allowlist. Its effective policy
must allow `mcp(pharos-ask/*)`, without conflicting ask/deny rules or strict mode.
Pharos combines `permissions` from `~/.gemini/antigravity-cli/settings.json`
with `userSettings.globalPermissionGrants` from `~/.gemini/config/config.json`.
Matching deny and ask rules take precedence over allow rules from either file.
Pharos checks that policy before sending private context and points to
Antigravity's `/permissions` command for a missing grant. It never broadens the
user's global permissions automatically.

The agent receives a short list of the top-level sections (home, search,
cards, analyze, findings, TL1 and the settings pages) with ids, recent
conversation context, local time, and a private lookup protocol on
stdin/stdout. It can request up to two bounded lookups of work, page visits,
or earlier chats. Pharos services them inside that request. There is no
public MCP tool, shared connector token, or separate listener for private
history. The agent can additionally use Pharos MCP tools to inspect archived
work, conversations, findings, and statistics, and is instructed to report
connection failures instead of inventing evidence. Private visits and history
stay on the lookup protocol.

The prompt no longer lists every card. For anything more specific than a
section, the agent browses the [site map](site-map.md) with the MCP tools
`browse_site` and `site_link`, and returns the URL `site_link` gave it. It is
told to prefer the exact view asked for (dashboard, chart, split, filters
and window) over the closest prebuilt card, and to say that no view can show
something only after browsing confirms it.

Only when the agent has already chosen a Query Table link as the destination is
it encouraged to add a useful graph through Query Table's metric builder, even
when the user did not explicitly request one. This encouragement does not apply
to dashboard links: dashboards already offer higher-quality graphics. The agent
keeps a suitable dashboard destination and its existing charts instead of switching
to a Query Table link just to add a metric.
It browses the page's query schema and metric/display type help, adds a small
number of relevant `query.aggregations`, and passes the complete query to
`site_link`. Metrics use `scope: "allMatching"` over the table's filtered
population, preserving repository scope and the time window. Examples include
tool-call counts by tool as a bar chart or token totals over time as a line chart
with calendar grouping. A plain table or detail view still fits when no sensible
graph helps or the user asks only for records.

A destination is `{id, url, title, description}`, with exactly one of `id` and
`url` non-empty; Codex's strict structured output needs every property, so
the unused one is an empty string. At most three are returned. An `id` must
be a section, a lookup result, or a card from an earlier turn of the chat. A
`url` is read with `site_link`'s `{url}` path (`siteLinkFromURL`) in
process: Pharos accepts it only when the site map parses it into a state and
rebuilds it, and the card uses the rebuilt canonical URL. Non-empty agent titles
and descriptions replace the generated title and summary; empty values retain
the view's own text. Titles are limited to 300 bytes. When offering several
links, the agent is instructed to give them clearly distinct contextual names
and explain what each adds, highlighting differences in metric, grouping,
repository, time range or purpose instead of repeating generic names. Names
must describe the linked views faithfully. Its kind is the page's section.
Anything else, including other origins, unknown parameters, charts the page
would coerce, and `ask_chat`
links, is left out of the answer, as is an unknown id, a destination without
exactly one of `id` and `url`, or an over-long description. The turn records
how many were left out (`omitted`) and the reply says so. Only a response
whose every destination was left out fails the request, with a retryable
error naming the first problem. A card's id is derived from
its canonical URL, so the same link in another parameter order is one card,
and a follow-up can name an earlier card by id. Saved chats keep the
canonical URL. This build does not generate arbitrary Query Table SQL: query
tables take native QueryState, including filters, grouping and metrics, as the
site map describes.

Chats and drafts live in the catalog and can be reopened by `/?ask_chat=ID`.
History supports text search and pagination. Chats follow the library between
Macs; continuing one retains its original model selection. Consent and visit
history are keyed to the current host. The visit store retains at most 1,000
observed URLs, titles and timestamps, including query/filter state, and imports
the existing browser history after opt-in. Removing or clearing browser history
also removes the corresponding private visit records. Missing history is never
presented as proof that a view was unseen.

A visits lookup names each visit's view three ways. `viewKey` ignores
parameter order but keeps query and filter state; for a link the site map
reads, it is the key of the canonical link, so older names compare equal.
`page` and `card` are the site map's page path and card id, where it reads
one. The lookup also lists `seenViewKeys`, `seenPages` and `seenCards`, so
the agent compares the pages and cards `browse_site` offers with what the
user has opened. Two links can draw the same chart (a link that spells out
every chart setting and one that leaves defaults out), so `viewKey`
equality is not view equality; `seenPages` and `seenCards` are the coarser
comparison. Reading a card's link compares it with every card, a few
milliseconds each, so the views of visited URLs are remembered in memory.

Only user-visible questions, replies and cards enter the general conversation
index. Private lookup payloads do not. Before sending private context to
Antigravity, Pharos registers its native conversation identity so ordinary source
ingestion excludes that retained CLI transcript. Catalog schema 15 prevents
older Pharos versions from ingesting around that exclusion. Reopened chats never
replay automatic navigation; interrupted or uncertain requests are reconciled
with saved state before retrying.

Runtime endpoints are under `/api/ask/` and use Pharos's existing authenticated,
same-origin loopback boundary. They are deliberately absent from the MCP tool
registry and general query datasets. The design-review prototype remains a
separate simulated review surface.

Ask streams progress to the UI while Codex, Claude, or Antigravity is running. Codex uses
[`exec --json`](https://learn.chatgpt.com/docs/non-interactive-mode#make-output-machine-readable);
Claude uses [`--output-format stream-json --verbose --include-partial-messages`](https://code.claude.com/docs/en/headless#stream-responses).
Antigravity uses [`--output-format stream-json`](https://antigravity.google/docs/cli/headless/#streaming-json),
reading `step_update` transitions for tool calls, response generation, and checkpoints.
The browser requests newline-delimited JSON from
`/api/ask/send`: `progress` events carry a timestamped activity label, and a
`result` event carries the complete validated session. Clients without that
Accept header retain the single JSON response. Partial model output is never
used as a destination or final answer.

The waiting indicator shows the latest action and elapsed time, including time
since the last update after ten seconds of silence. An expandable Activity list
is shown only for failed requests with recorded activity. It retains the latest
40 updates, including startup, Pharos tool calls, private
lookups, and answer saving. Labels are authored by Pharos; raw reasoning, model
text, tool arguments/results, and CLI diagnostics do not enter the feed.
Antigravity's `call_mcp_tool` routing identifies the Pharos operation, and its
`tool_info.error` marks failed calls. Repeated active steps do not duplicate
activity; routing details arriving later can refine a generic tool label.
Running activity is held in memory and included when reopening a chat; it is saved with the finished
turn, outside the conversation search index. A broken stream uses the existing
session reconciliation path and never automatically repeats inference.
