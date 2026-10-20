// Agent Markdown in the conversation reader. markdownTree (reader-model.js)
// parses it into elements and text that the page builds with text nodes and
// element properties only (markdownView in ui.py), so this checks the safety
// guard: embedded HTML never becomes markup and links keep only safe schemes.
import { test } from "node:test";
import assert from "node:assert/strict";
import { reader } from "../helpers/transcript.mjs";

const { markdownLinkAllowed, markdownTree } = reader;
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
  const allowed = ["https://example.com/a", "http://example.com", "HTTPS://EXAMPLE.COM", "mailto:team@example.com", "#notes", "/library", "./docs/a.md", "../b.md"];
  const refused = ["javascript:alert(1)", "JavaScript:alert(1)", "vbscript:msgbox(1)", "data:text/html;base64,PHNjcmlwdD4=", "//evil.example/x", "file:///etc/passwd", "ftp://example.com", "alert(1)"];
  for (const href of allowed) assert.equal(markdownLinkAllowed(href), true, href);
  for (const href of refused) assert.equal(markdownLinkAllowed(href), false, href);
  for (const href of [...allowed, ...refused]) {
    const links = elements(markdownTree(`See [it](${href}) and ![image](${href}).`)).filter(element => element.tag === "a");
    assert.deepEqual(links.map(link => link.href), markdownLinkAllowed(href) ? [href, href] : [], href);
    // Only web links open a new tab.
    assert.ok(links.every(link => (link.target === "_blank") === /^https?:/.test(href)), href);
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
