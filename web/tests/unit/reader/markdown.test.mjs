// Agent Markdown in the conversation reader. markdownTree (reader-model.js)
// parses it into elements and text that the page builds with text nodes and
// element properties only (markdownView in ui.py), so this checks the safety
// guard: embedded HTML never becomes markup and links keep only safe schemes.
import { test } from "node:test";
import assert from "node:assert/strict";
import { reader } from "../helpers/transcript.mjs";

const { markdownLinkAllowed, markdownLinkKind, markdownTree } = reader;
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
  for (const href of ["/", "/library", "/library?search=cache&view=conversation", "/work/work%2Fid", "/work/work-1/conversation/session-1?message=prompt", "/analyze?analyze_topic=tokens", "/settings/ask", "/conversation/legacy", "?search=cache"]) {
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

test("missing classification, automated prompts and invalid ranges do not imply human writing", () => {
  for (const message of [
    { role: 'user', text: 'Unknown provenance.' },
    { role: 'user', text: 'Agent assignment.', authorship: { spans: [{ category: 'automated', start: 0, end: 17 }] } },
    { role: 'assistant', text: 'Agent response.', authorship: { spans: [{ category: 'typed', start: 0, end: 15 }] } },
    { role: 'user', text: 'Old spans.', authorship: { typed_words: 2, spans: [{ category: 'typed', excerpt: 'Old spans.' }] } },
    { role: 'user', text: 'Invalid.', authorship: { spans: [{ category: 'typed', start: -1, end: 200 }] } },
  ]) assert.equal(reader.hasHumanWriting(message), false, JSON.stringify(message));
});
