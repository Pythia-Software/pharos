// Agent Markdown in the conversation reader. markdownTree (reader-model.js)
// parses it into elements and text that the page builds with text nodes and
// element properties only (markdownView in ui.py), so this checks the safety
// guard: embedded HTML never becomes markup and links keep only safe schemes.
import { test } from "node:test";
import assert from "node:assert/strict";
import { reader } from "../helpers/transcript.mjs";
import { plain } from "../helpers/classic-script.mjs";

const { markdownLinkAllowed, markdownLinkChecker, markdownLinkKind, markdownLinkProbed, markdownTree } = reader;
const escape = text => String(text).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
// The HTML the page's markdownView produces for a tree.
function html(item) {
  if (typeof item === "string") return escape(item);
  if (item.icon) return `<svg data-icon="${escape(item.icon)}"></svg>`;
  const attributes = [["class", item.className], ...["href", "rel", "target", "start"].map(key => [key, item[key]]), ["data-language", item.language]]
    .filter(([, value]) => value !== undefined && value !== "").map(([key, value]) => ` ${key}="${escape(value)}"`).join("");
  return `<${item.tag}${attributes}>${item.children.map(html).join("")}</${item.tag}>`;
}
function elements(item, found = []) {
  if (item && typeof item === "object" && item.tag) { found.push(item); for (const child of item.children) elements(child, found); }
  return found;
}
const text = item => typeof item === "string" ? item : (item.children || []).map(text).join("");
const allowedTags = new Set(["div", "p", "h1", "h2", "h3", "h4", "h5", "h6", "strong", "em", "del", "code", "pre", "ul", "ol", "li", "blockquote", "table", "thead", "tbody", "tr", "th", "td", "a", "span", "hr"]);

test("compact message excerpts keep prose beyond harness setup and flatten Markdown", () => {
 const preview = text => reader.compactMessageSummary({ role: "user", text });
 assert.equal(preview('<system_instruction>Workspace setup</system_instruction>\n\nPlease **fix** [the parser](https://example.com/docs).'), "Please fix the parser.");
 assert.equal(preview("# Result\n\n**Fixed** the `parser`.\n\n- Tests pass\n- Ready to review"), "Result Fixed the parser. Tests pass Ready to review");
 const long = preview("Please review the parser and check its callers. ".repeat(20));
 assert.ok(long.length <= 640); assert.ok(long.endsWith("…"));
 assert.equal(preview(""), "");
 assert.equal(preview("Swim at \uE200entity\uE202[\"t0\",\"Grand Lake\"]\uE201.\uE200cite\uE202t1\uE201"), "Swim at Grand Lake.");
});

test("agent Markdown renders structure without activating embedded HTML or unsafe links", () => {
  const markdown = "# Findings\n\n**Passed** with *one caveat* and `inlineCode`.\n\n- First item\n- Second item\n  - Nested item\n\n> A quoted observation\n\n| Name | Result |\n| --- | --- |\n| parser | pass |\n\n```python\nprint(\"ok\")\n```\n\n[Docs](https://example.com/docs)\n\n<script>window.markdownExecuted=true</script>\n\n[bad](javascript:alert(1))";
  const tree = markdownTree(markdown), found = elements(tree), named = tag => found.filter(element => element.tag === tag);
  assert.deepEqual(named("h1").map(text), ["Findings"]);
  assert.deepEqual(named("strong").map(text), ["Passed"]);
  assert.equal(named("li").length, 3);
  assert.equal(named("tbody").flatMap(body => elements(body)).filter(element => element.tag === "td").length, 2);
  assert.equal(named("pre").length, 1);
  assert.match(text(named("pre")[0]), /print/);
  assert.equal(named("code").find(element => element.language)?.language, "python");
  assert.equal(named("blockquote").length, 1);
  const links = named("a");
  assert.equal(links.length, 1);
  assert.deepEqual([links[0].href, links[0].rel, links[0].target, text(links[0])], ["https://example.com/docs", "noreferrer noopener", "_blank", "Docs"]);
  assert.ok(found.every(element => allowedTags.has(element.tag)), [...new Set(found.map(element => element.tag))].join(","));
  const page = html(tree);
  assert.doesNotMatch(page, /<script/i);
  assert.match(page, /&lt;script&gt;window\.markdownExecuted=true&lt;\/script&gt;/, "embedded HTML stays text");
  assert.doesNotMatch(page, /href="javascript:/i);
  assert.match(page, /<span>bad<\/span>/, "an unsafe link keeps its text without an href");
});

test("links keep only web, mail, fragment and relative destinations", () => {
  const allowed = ["https://example.com/a", "http://example.com", "HTTPS://EXAMPLE.COM", "mailto:team@example.com", "#notes", "/library", "./docs/a.md", "../b.md", "README.md", "file:///tmp/report.pdf", "file://localhost/tmp/report.pdf", "/tmp/report.md:12:3", "~/notes.md", "README.md:12:3"];
  const refused = ["javascript:alert(1)", "JavaScript:alert(1)", "vbscript:msgbox(1)", "data:text/html;base64,PHNjcmlwdD4=", "//evil.example/x", "ftp://example.com", "alert(1)"];
  for (const href of allowed) assert.equal(markdownLinkAllowed(href), true, href);
  for (const href of refused) assert.equal(markdownLinkAllowed(href), false, href);
  for (const href of [...allowed, ...refused]) {
    const links = elements(markdownTree(`See [it](${href}) and ![image](${href}).`)).filter(element => element.tag === "a");
    assert.deepEqual(links.map(link => link.href), markdownLinkAllowed(href) ? [href, href] : [], href);
    // File targets are opened by the checked local-file endpoint.
    assert.ok(links.every(link => (link.target === "_blank") === ["web", "server", "file"].includes(markdownLinkKind(href)?.kind)), href);
  }
});

test("raw HTML in any block or span stays text", () => {
  const attacks = ["<img src=x onerror=alert(1)>", "<a href=\"javascript:alert(1)\">x</a>", "<iframe src=\"https://example.com\"></iframe>", "<svg onload=alert(1)>"];
  for (const attack of attacks) {
    for (const markdown of [attack, `# ${attack}`, `- ${attack}`, `> ${attack}`, `**${attack}**`, `| ${attack} | x |\n| --- | --- |\n| ${attack} | y |`, "```\n" + attack + "\n```", `\`${attack}\``]) {
      const tree = markdownTree(markdown), found = elements(tree);
      assert.ok(found.every(element => allowedTags.has(element.tag)), markdown);
      assert.ok(found.filter(element => element.tag === "a").length === 0, markdown);
      assert.ok(text(tree).includes(attack.split(/[\s>]/)[0]), markdown);
      assert.doesNotMatch(html(tree), /<(img|iframe|svg|script)\b/i, markdown);
    }
  }
});

test("deep nesting falls back to text", () => {
  const nested = Array.from({ length: 20 }, () => ">").join(" ") + " <b>deep</b>";
  const found = elements(markdownTree(nested));
  assert.ok(found.filter(element => element.tag === "blockquote").length <= 13);
  assert.ok(found.every(element => allowedTags.has(element.tag)));
  assert.match(text(markdownTree(nested)), /<b>deep<\/b>/);
});

test("Markdown links distinguish local files, local servers, websites and page actions", () => {
  for (const href of ["docs/readme.md", "../README.md", "/Users/me/report.md:12", "file:///tmp/report.pdf", "~/notes.md"]) {
    assert.equal(markdownLinkKind(href).kind, "file", href);
  }
  for (const href of ["http://localhost:3000", "http://app.localhost:5173/", "http://127.0.0.2:8080/page", "http://[::1]:3000", "http://0.0.0.0:3000"]) {
    assert.equal(markdownLinkKind(href).kind, "server", href);
  }
  for (const href of ["https://github.com/org/repo", "https://localhost.example.com", "https://example.com:8443"])
    assert.equal(markdownLinkKind(href).kind, "web", href);
  assert.equal(markdownLinkKind("#notes").kind, "fragment");
  for (const href of ["/", "/library", "/library?search=cache&view=conversation", "/work/work%2Fid", "/work/work-1/conversation/session-1?message=prompt", "/analyze?analyze_topic=tokens", "/settings/ask", "/settings/projects", "/settings/models", "/conversation/legacy", "?search=cache"]) {
    assert.equal(markdownLinkKind(href).kind, "page", href);
    const link = elements(markdownTree(`[Pharos](${href})`)).find(item => item.tag === "a");
    assert.equal(link.href, href); assert.equal(link.target, undefined, "in-app navigation stays in the current tab");
  }
  for (const href of ["/library.md", "/settings/config.toml", "/Users/me/library", "/tmp/report.md"]) assert.equal(markdownLinkKind(href).kind, "file", href);
  assert.equal(markdownLinkKind("mailto:me@example.com").kind, "mail");
  for (const href of ["https://me:secret@example.com", "https://", "//evil.test", "javascript:alert(1)", "data:text/html,x", "x\\y.md", "./x\u0000.md"]) assert.equal(markdownLinkKind(href), null, href);
  const links = elements(markdownTree('[Report](</Users/me/My Report.md:12>) and [docs](docs/readme.md)')).filter(item => item.tag === 'a');
  assert.deepEqual(links.map(link => [link.href, link.link.kind]), [["/Users/me/My Report.md:12", "file"], ["docs/readme.md", "file"]]);
});

test("only local destinations are probed, each once a minute and four at a time", async () => {
  for (const href of ["docs/readme.md", "/Users/me/report.md:12", "http://localhost:5173/"]) assert.equal(markdownLinkProbed(markdownLinkKind(href)), true, href);
  for (const href of ["/library?view=conversation", "/analyze?analyze_topic=tokens", "#notes", "mailto:me@example.com", "https://example.com"]) assert.equal(markdownLinkProbed(markdownLinkKind(href)), false, href);
  assert.equal(markdownLinkProbed(null), false);
  const calls = [];
  let clock = 0;
  const check = markdownLinkChecker((href, workspace) => new Promise((resolve, reject) => calls.push({ href, workspace, resolve, reject })), { now: () => clock });
  const first = check("internal/archive/api.go:12", "w1");
  assert.equal(check("internal/archive/api.go:12", "w1"), first, "a repeated link shares its check");
  check("internal/archive/api.go:12", "w2");
  for (const href of ["a.md", "b.md", "c.md"]) check(href, "w1");
  assert.deepEqual(calls.map(call => [call.href, call.workspace]), [["internal/archive/api.go:12", "w1"], ["internal/archive/api.go:12", "w2"], ["a.md", "w1"], ["b.md", "w1"]], "a workspace has its own check, and only four run at once");
  calls[0].resolve({ status: "available", href: "file:///x" });
  assert.deepEqual(await first, { status: "available", href: "file:///x" });
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(calls.length, 5, "a finished check starts the next");
  calls[1].reject(new Error("offline"));
  assert.equal((await check("internal/archive/api.go:12", "w2")).status, "unknown", "a failed check reports unknown");
  clock = 60001;
  check("internal/archive/api.go:12", "w1");
  assert.equal(calls.length, 6, "after a minute the link is checked again");
});

test("human writing follows UTF-8 authorship spans through Markdown formatting", () => {
  const parts = [
    ['<system_instruction>Generated context</system_instruction>\n\n', 'harness'],
    ['# Please **fix** the café 🐚 parser\n\n', 'typed'],
    ['- Keep the ', 'typed'], ['`cached_value`', 'pasted'], [' correct\n  - Check [the docs](', 'typed'],
    ['https://example.com/docs', 'pasted'], [')\n\n> Preserve this note\n\n| Item | Result |\n| --- | --- |\n| résumé | good |\n\n```text\nhuman-written example\n```', 'typed'],
  ];
  let end = 0;
  const spans = parts.map(([text, category]) => { const start = end; end += Buffer.byteLength(text); return { start, end, category }; });
  const source = parts.map(([text]) => text).join('');
  const message = { role: 'user', text: source, authorship: { spans } };
  const mask = reader.humanWritingMask(message);
  const tree = reader.markdownTree(source, 0, mask), found = elements(tree);
  const highlighted = found.filter(item => item.tag === 'mark').map(text).join('');
  assert.match(highlighted, /café 🐚 parser/);
  assert.match(highlighted, /résumé/);
  assert.match(highlighted, /human-written example/);
  assert.doesNotMatch(highlighted, /Generated context|cached_value|https:/);
  assert.ok(found.some(item => item.tag === 'strong' && text(item) === 'fix'));
  assert.ok(found.some(item => item.tag === 'a' && item.href === 'https://example.com/docs' && text(item) === 'the docs'));
  assert.equal(text(tree), text(reader.markdownTree(source)), 'highlighting preserves every rendered character');
});

test("human writing projects escaped JSON text and survives message normalization", () => {
  const display = 'Please fix **café 🐚**.\r\nKeep "this" working.';
  const source = JSON.stringify({ role: 'user', content: [{ type: 'text', text: display }] });
  const literal = JSON.stringify(display), start = source.indexOf(literal) + 1;
  const message = { native_id: 'human', role: 'user', kind: 'message', text: source,
    authorship: { spans: [{ category: 'typed', start: Buffer.byteLength(source.slice(0, start)), end: Buffer.byteLength(source.slice(0, start + literal.length - 2)) }] } };
  const normalized = reader.normalizeMessages([message])[0];
  assert.equal(normalized.text, display);
  const mask = reader.humanWritingMask(normalized);
  assert.equal(mask, '1'.repeat(display.length));
  const marked = elements(reader.markdownTree(display, 0, mask)).filter(item => item.tag === 'mark').map(text).join('');
  assert.match(marked, /café 🐚/);
  assert.match(marked, /Keep "this" working\./);
  const ambiguous = { ...message, text: JSON.stringify({ first: display, second: display }) };
  assert.equal(reader.humanWritingMask(ambiguous, display), '', 'ambiguous source text must not invent attribution');
});

test("local labels describe the whole message while writing and event marks keep their own spans", () => {
  const taxonomy = [{ id: 'P.control', name: 'Steer ongoing work' }, { id: 'P.control.continue', name: 'Continue', definition: 'Keep going.' },
    { id: 'P.request.work', name: 'Request work' }];
  const typed = 'Sync to main, then **continue**.', source = '<context>Pasted notes</context>\n' + typed;
  const message = { role: 'user', kind: 'message', text: source,
    authorship: { spans: [{ category: 'typed', start: Buffer.byteLength(source) - Buffer.byteLength(typed), end: Buffer.byteLength(source) }] },
    labels: { segment: 'H-typed', ids: ['P', 'P.request', 'P.request.work', 'P.control', 'P.control.continue'], probs: { P: 0.99, 'P.request.work': 0.37, 'P.control.continue': 0.98 } } };
  const view = reader.messageLabelView(message, taxonomy);
  assert.equal(view.family, 'P.control', 'the likeliest specific label picks the hue');
  assert.deepEqual(plain(view.labels.map(label => [label.id, label.title, label.prob])), [['P.control.continue', 'Control · Continue', 0.98], ['P.request.work', 'Request work', 0.37]]);
  assert.equal(view.segmentName, 'Typed text');
  // Labels are card metadata; they do not create text-level marks.
  assert.equal(reader.humanWritingMask(message, source).includes('1'), true);
  const reply = { role: 'assistant', kind: 'message', text: 'All **checks** pass.', labels: { segment: 'AR', ids: ['C', 'C.report', 'C.report.completion'], probs: {} } };
  assert.equal(reader.humanWritingMask(reply, reply.text), '');
  assert.equal(reader.messageLabelView(reply).labels[0].title, 'Report · C.report.completion', 'unnamed labels fall back to their ids, prefixed by family');
  // Unlabeled messages mark nothing; a root alone has no family.
  assert.equal(reader.messageLabelView({ role: 'assistant', text: 'Done.' }), null);
  const root = reader.messageLabelView({ labels: { ids: ['C'], probs: {} } });
  assert.deepEqual(plain([root.family, root.labels[0].title]), ['', 'Responds or reports']);
});

test("the reader offers each highlight the conversation has marks for, each shown on its own", () => {
  const typed = 'Please fix it.', reply = 'The endpoint is down.';
  const human = { role: 'user', kind: 'message', text: typed, authorship: { spans: [{ category: 'typed', start: 0, end: typed.length }] } };
  const agent = { role: 'assistant', kind: 'message', text: reply, labels: { segment: 'AR', ids: ['C', 'C.report', 'C.report.completion'], probs: {} },
    agent_events: [{ kind: 'obstacle', label: 'Obstacle', group: 'Friction', probability: 0.9, sentence: [0, reply.length], subject: [], response: [] }] };
  const conversation = { labels: { labeled: true, pending: false }, agent_events: { messages_read: 1 } };
  const options = reader.readerHighlightOptions(conversation, [human, agent]);
  assert.deepEqual(plain(options.map(option => [option.id, option.field, option.available])),
    [['writing', 'reader_writing', true], ['events', 'reader_events', true], ['labels', 'reader_labels', true]]);
  assert.deepEqual(plain(options[1].legend), [{ name: 'Friction', className: 'event-friction' }]);
  assert.equal(options[1].note, '');
  // Events awaiting a rebuild stay inspectable here, but never read as current.
  const rebuilding = reader.readerHighlightOptions({ ...conversation, agent_events: { messages_read: 1, pending: true } }, [human, agent]);
  assert.match(rebuilding[1].note, /^Waiting to be rebuilt .* may change$/);
  assert.deepEqual(plain(options[2].legend), [{ name: 'Report', family: 'C.report' }]);
  // Writing and classifier marks are on by default; a link
  // wins over the saved choice, which wins over the default.
  const shown = (linked, saved) => [...reader.readerHighlightsShown(options, linked, saved)].sort();
  assert.deepEqual(shown(), ['events', 'labels', 'writing']);
  assert.deepEqual(shown(() => null, pref => pref === 'pharos-reader-events'), ['events']);
  assert.deepEqual(shown(field => field === 'reader_labels' ? true : field === 'reader_writing' ? false : null), ['events', 'labels']);
  // Human writing is offered only where there is some, and a classifier only
  // once it has run; one with nothing here is offered but off.
  const plainOptions = reader.readerHighlightOptions({ labels: { labeled: false, pending: true }, agent_events: { messages_read: 3 } }, [{ role: 'assistant', kind: 'message', text: 'Done.' }]);
  assert.deepEqual(plain(plainOptions.map(option => [option.id, option.available, option.note])),
    [['events', false, ''], ['labels', false, 'Not labeled yet']]);
  assert.deepEqual(shown(() => true), ['events', 'labels', 'writing']);
  const overlapping=[{group:'Friction',probability:.9},{group:'Reactions and learning',probability:.8}];
  assert.deepEqual(plain(reader.readerEventHighlights(overlapping,new Set(['events:event-learning']))),[overlapping[1]],'hiding the strongest group leaves the other hue and hover content');
  assert.equal(reader.readerEventHighlights(overlapping,new Set()).length,0);

  assert.equal(reader.readerHighlightsShown(plainOptions, () => true).size, 0);
  assert.deepEqual(plain(reader.readerHighlightOptions({}, [human]).map(option => option.id)), ['writing']);
  assert.deepEqual(plain(reader.readerHighlightOptions({}, [agent])), []);
});

test("agent events mark each event's sentence by group and its context, in UTF-16 offsets", () => {
  // An emoji is two UTF-16 units: offsets count them as JavaScript does.
  const body = "The 🚧 endpoint doesn't support pagination. I'll work around it by fetching one day at a time. **Done.**";
  const at = sentence => [body.indexOf(sentence), body.indexOf(sentence) + sentence.length];
  const blocked = at("The 🚧 endpoint doesn't support pagination."), workaround = at("I'll work around it by fetching one day at a time.");
  const message = { role: 'assistant', kind: 'message', text: body, agent_events: [
    { kind: 'workaround', label: 'Workaround', group: 'Choices you may not see', probability: 0.91, sentence: workaround, subject: [blocked], response: [workaround] },
    { kind: 'missing_capability', label: 'Missing tool or capability', group: 'Friction', probability: 0.62, sentence: blocked, subject: [], response: [] },
    { kind: 'obstacle', label: 'Obstacle or roadblock', group: 'Friction', probability: 0.88, sentence: blocked, subject: [], response: [] },
    { kind: 'surprise', label: 'Surprise', group: 'Reactions and learning', probability: 0.7, sentence: [0, body.length + 4], subject: [], response: [] },
  ] };
  const view = reader.messageEventView(message);
  assert.deepEqual(plain(view.map(event => event.kind)), ['workaround', 'missing_capability', 'obstacle'], 'a range past the text is dropped');
  assert.deepEqual(plain([view[0].sentenceText, view[0].subjectText, view[0].responseText]),
    ["I'll work around it by fetching one day at a time.", "The 🚧 endpoint doesn't support pagination.", "I'll work around it by fetching one day at a time."]);
  // Each marked sentence is a mark of its own, naming its span; an event's
  // sentence keeps its group's mark over another event's context.
  const marksOf = events => elements(reader.markdownTree(body, 0, reader.eventMask(body, events, ''))).filter(item => item.tag === 'mark');
  assert.deepEqual(plain(marksOf(view).map(item => [item.className, text(item)])), [
    ['agent-event event-friction event-at-0', "The 🚧 endpoint doesn't support pagination."],
    ['agent-event event-choices event-at-1', "I'll work around it by fetching one day at a time."],
  ]);
  // Hovering a mark names the events of its own sentence, Markdown inside it
  // included: "It is **not** done." is one mark around the bold, and names no
  // other sentence's events.
  const [first, second] = marksOf(view);
  assert.deepEqual(plain(reader.eventsAtMark(view, first).map(event => event.kind)), ['missing_capability', 'obstacle']);
  assert.deepEqual(plain(reader.eventsAtMark(view, second).map(event => event.kind)), ['workaround']);
  const notMessage = { ...message, text: "It is **not** done. It was not run." };
  const notView = reader.messageEventView({ ...notMessage, agent_events: [
    { kind: 'unverified', label: 'Unchecked claim', group: 'Choices you may not see', probability: 0.8, sentence: [20, 35], subject: [], response: [] },
    { kind: 'deferred', label: 'Deferred', group: 'Choices you may not see', probability: 0.7, sentence: [0, 19], subject: [], response: [] }] });
  const notMarks = elements(reader.markdownTree(notMessage.text, 0, reader.eventMask(notMessage.text, notView, ''))).filter(item => item.tag === 'mark');
  const notPiece = notMarks.find(item => text(item) === 'It is not done.');
  assert.ok(notPiece?.children.some(child => child.tag === 'strong'), 'the bold sits inside the mark');
  assert.deepEqual(plain(reader.eventsAtMark(notView, notPiece).map(event => event.kind)), ['deferred']);
  // Context that is no event's sentence is marked lighter and names the
  // events it is context of; the likelier of two events on a sentence picks
  // its hue.
  const context = reader.messageEventView({ ...message, agent_events: [{ ...message.agent_events[0], subject: [at("**Done.**")] }, { ...message.agent_events[2], group: 'Worth a second look', probability: 0.95 }, message.agent_events[1]] });
  const contextMarks = marksOf(context);
  assert.deepEqual(plain(contextMarks.map(item => item.className)), ['agent-event event-second-look event-at-0', 'agent-event event-choices event-at-1', 'event-context event-at-1']);
  assert.deepEqual(plain(reader.eventsAtMark(context, contextMarks[2]).map(event => event.kind)), ['workaround']);
  assert.deepEqual(plain(reader.eventsAtMark(view, { className: 'agent-event event-friction' })), []);
  // Only the text that was scored is marked: Codex reasoning keeps its JSON
  // event in raw_text and its prose in text, and is marked; a block split out
  // of a JSON message loses the events (its offsets are the whole message's).
  assert.equal(reader.messageEventView({ ...message, kind: 'reasoning', raw_text: JSON.stringify({ type: 'reasoning', text: body }) }).length, 3);
  const json = JSON.stringify({ message: { role: 'assistant', content: [{ type: 'text', text: "I'll work around it." }, { type: 'text', text: body }] } });
  const split = reader.normalizeMessages([{ ...message, id: 'm-split', text: json }]);
  assert.equal(split.length, 2);
  assert.ok(split.every(entry => entry.agent_events === undefined && reader.messageEventView(entry) === null));
  assert.equal(reader.normalizeMessages([message])[0].agent_events.length, 4, 'a message shown as stored keeps its events');
  assert.equal(reader.messageEventView({ ...message, text: JSON.stringify({ text: body }) }), null);
  assert.equal(reader.messageEventView({ role: 'assistant', text: 'Done.' }), null);
  assert.equal(reader.eventMask('Done.', null, '01010'), '01010');
  // Thinking is shown as its own text, so its events apply to it.
  assert.equal(reader.messageEventView({ ...message, kind: 'reasoning' }).length, 3);
});

test("missing classification, automated prompts and invalid ranges do not imply human writing", () => {
  for (const message of [
    { role: 'user', text: 'Unknown provenance.' },
    { role: 'user', text: 'Agent assignment.', authorship: { spans: [{ category: 'automated', start: 0, end: 17 }] } },
    { role: 'assistant', text: 'Agent response.', authorship: { spans: [{ category: 'typed', start: 0, end: 15 }] } },
    { role: 'user', text: 'Old spans.', authorship: { typed_words: 2, spans: [{ category: 'typed', excerpt: 'Old spans.' }] } },
    { role: 'user', text: 'Invalid.', authorship: { spans: [{ category: 'typed', start: -1, end: 200 }] } },
  ]) assert.equal(reader.hasHumanWriting(message), false, JSON.stringify(message));
});

// A highlight across inline code, emphasis or a link is one <mark>: an
// element the highlight covers whole moves inside it; one it starts or ends
// inside keeps its own piece.
test("a highlight crossing inline markdown is one mark", () => {
  const body = "Before. hello world `this is markdown` another *thing* [link](https://example.com) done. After.";
  const said = "hello world `this is markdown` another *thing* [link](https://example.com) done.";
  const event = { id: "x", kind: "obstacle", label: "Obstacle", group: "Friction", probability: 0.9, sentence: [body.indexOf(said), body.indexOf(said) + said.length], subject: [], response: [] };
  const view = reader.messageEventView({ role: "assistant", kind: "message", text: body, agent_events: [event] });
  const tree = markdownTree(body, 0, reader.eventMask(body, view, "")), marks = elements(tree).filter(element => element.tag === "mark");
  assert.equal(marks.length, 1);
  assert.equal(html(marks[0]), '<mark class="agent-event event-friction event-at-0">hello world <code>this is markdown</code> another <em>thing</em> <a class="markdown-link" href="https://example.com" rel="noreferrer noopener" target="_blank">link</a> done.</mark>');
  // A target ending inside the code: the covered text is one piece, the code keeps its own.
  const prompt = "Keep `the drawer only` path.", start = prompt.indexOf("Keep"), end = prompt.indexOf(" only");
  const mask = reader.eventTargetMask(prompt, [{ range: [start, end], links: ["e"] }], "");
  assert.equal(html(markdownTree(prompt, 0, mask)), '<div class="markdown-content"><p><mark class="event-target event-target-at-0">Keep </mark><code><mark class="event-target event-target-at-0">the drawer</mark> only</code> path.</p></div>');
  // Adjacent different highlights stay apart.
  const both = "one `two` three", split = "1".repeat(4) + "2".repeat(11);
  assert.deepEqual(elements(markdownTree(both, 0, split)).filter(element => element.tag === "mark").map(mark => mark.className), ["human-written", "labeled-text"]);
});

test("an export's inline markers read as words, and highlights stay on theirs", () => {
  const { exportMarkerText } = reader;
  const E = "", P = "", X = "";
  const text = `Go to ${E}entity${P}["turn0business0","Wellington Lake"]${X} today.${E}cite${P}turn1search2${X} ${E}genui${P}{"map_widget":{}}${X}`;
  assert.deepEqual(plain(exportMarkerText(text)), { text: "Go to Wellington Lake today. [map widget]", mask: "" });
  // The flag on "today" follows the word across the cut.
  const at = text.indexOf("today"), mask = "0".repeat(at) + "11111" + "0".repeat(text.length - at - 5);
  const shown = exportMarkerText(text, mask);
  assert.equal(shown.mask.length, shown.text.length);
  assert.equal(shown.mask.indexOf("11111"), shown.text.indexOf("today"));
  assert.deepEqual(plain(exportMarkerText("No markers", "0100000000")), { text: "No markers", mask: "0100000000" });
});

test("source text is cut into comments, strings, numbers and keywords by its extension", () => {
  const { highlightCode } = reader, runs = (text, name) => plain(highlightCode(text, name)).filter(run => run.kind).map(run => [run.kind, run.text]);
  const script = "// total\nconst sum = add(1, 'a//b'); /* done */";
  assert.deepEqual(runs(script, "points.js"), [["comment", "// total"], ["keyword", "const"], ["number", "1"], ["string", "'a//b'"], ["comment", "/* done */"]]);
  assert.deepEqual(runs("# note\ndef area(r):\n    return 3.14 * r  # pi", "shape.py"), [["comment", "# note"], ["keyword", "def"], ["keyword", "return"], ["number", "3.14"], ["comment", "# pi"]]);
  assert.deepEqual(runs("SELECT id FROM t -- all", "q.sql"), [["keyword", "SELECT"], ["keyword", "FROM"], ["comment", "-- all"]]);
  assert.deepEqual(runs('<p class="x">hi</p><!-- end -->', "page.html"), [["keyword", "<p"], ["string", '"x"'], ["keyword", ">"], ["keyword", "</p>"], ["comment", "<!-- end -->"]]);
  // Every character is kept, in order, whatever the language; an unknown one is left plain.
  for (const name of ["points.js", "shape.py", "q.sql", "page.html", "notes.txt"]) assert.equal(plain(highlightCode(script, name)).map(run => run.text).join(""), script);
  assert.deepEqual(plain(highlightCode(script, "notes.txt")), [{ kind: "", text: script }]);
});
