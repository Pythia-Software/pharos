// Harness XML blocks (<environment_context>, <system-reminder>, …) in message
// text: which blocks become chips, the tree a chip expands to, and message
// summaries (reader-model.js).
import { test } from "node:test";
import assert from "node:assert/strict";
import { plain } from "../helpers/classic-script.mjs";
import { reader } from "../helpers/transcript.mjs";

const { proseSummary, xmlSegments, xmlTree } = reader;
const tags = text => plain([...xmlSegments(text)].filter(segment => segment.tag).map(segment => segment.tag));
const prose = text => plain([...xmlSegments(text)].filter(segment => !segment.tag).map(segment => segment.text));
// An element as its open tag, its text and its child elements.
const shape = element => ({ open: element.open, close: element.close, text: element.children.filter(child => typeof child === "string").join("").trim(), children: element.children.filter(child => typeof child !== "string").map(shape) });

test("harness XML blocks become chips while surrounding text stays prose", () => {
  const ask = ["<environment_context>", "  <cwd>/workspace</cwd>", "  <shell>zsh</shell>", "</environment_context>", "<system_instruction>",
    "Save screenshots and embed them, for example `![Screenshot](</Users/me/shot.png>)`.", "</system_instruction>", "Please **tidy** the renderer."].join("\n");
  assert.deepEqual(tags(ask), ["environment_context", "system_instruction"]);
  assert.deepEqual(prose(ask), ["Please **tidy** the renderer."]);
  assert.match(xmlSegments(ask)[1].text, /<\/Users\/me\/shot\.png>/, "a stray < inside a block stays in it");
  assert.equal(proseSummary(ask), "Please **tidy** the renderer.");
  assert.deepEqual(plain(shape(xmlTree(xmlSegments(ask)[0].text))), { open: "<environment_context>", close: "</environment_context>", text: "", children: [
    { open: "<cwd>", close: "</cwd>", text: "/workspace", children: [] }, { open: "<shell>", close: "</shell>", text: "zsh", children: [] }] });

  const reply = ["<system-reminder note=\"x\">Keep going.</system-reminder>", "Done — see **the diff**.", "", "```xml", "<example>", "</example>", "```", "",
    "<div>unbalanced <b>markup</div>", "Use Array<string> here."].join("\n");
  assert.deepEqual(tags(reply), ["system-reminder"], "fenced code, unbalanced markup and generics stay prose");
  const [rest] = prose(reply);
  for (const kept of ["```xml\n<example>\n</example>\n```", "<div>unbalanced <b>markup</div>", "Array<string>"]) assert.ok(rest.includes(kept), kept);
  assert.match(proseSummary(reply), /^Done/);
});

test("blocks qualify only on their own lines", () => {
  assert.deepEqual(tags("Inline <note>text</note> stays prose."), []);
  assert.deepEqual(tags("<note>text</note> and more"), [], "a block must end its line");
  assert.deepEqual(tags("  <note>indented</note>\nafter"), ["note"]);
  assert.deepEqual(tags("<a><b></a></b>"), [], "crossed tags are not a block");
  assert.deepEqual(tags("<!-- comment -->"), [], "a block starts with an element");
  assert.deepEqual(tags("<empty/>"), [], "a self-closing tag is not a block");
  assert.deepEqual(tags("<outer>\n<inner/>\n<![CDATA[ <x> ]]>\n</outer>"), ["outer"]);
});

test("messages that are only XML summarize by tag", () => {
  const text = "<command-name>/review</command-name>\n<command-args>deep</command-args>";
  assert.deepEqual(tags(text), ["command-name", "command-args"]);
  assert.deepEqual(prose(text), []);
  assert.equal(proseSummary(text), "<command-name> <command-args>");
});

test("expanded XML chips keep nested elements as their own branches", () => {
  const text = "<context>\n  <files>\n    <file path=\"a.go\">one</file>\n    <file path=\"b.go\">two\nlines</file>\n  </files>\n  <note>kept</note>\n</context>\nGo.";
  assert.deepEqual(tags(text), ["context"]);
  assert.deepEqual(prose(text), ["Go."]);
  assert.deepEqual(plain(shape(xmlTree(xmlSegments(text)[0].text))), { open: "<context>", close: "</context>", text: "", children: [
    { open: "<files>", close: "</files>", text: "", children: [
      { open: "<file path=\"a.go\">", close: "</file>", text: "one", children: [] },
      { open: "<file path=\"b.go\">", close: "</file>", text: "two\nlines", children: [] }] },
    { open: "<note>", close: "</note>", text: "kept", children: [] }] });
});
