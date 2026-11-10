import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import test from "node:test";
import { loadClassicScript, plain } from "./helpers/classic-script.mjs";

const { dictationSpaced, dictationStep } = loadClassicScript("dictation.js");
const settle = () => new Promise(resolve => setImmediate(resolve));

test("dictated words are spaced from the text before them", () => {
  assert.equal(dictationSpaced("", " Hello there. "), "Hello there.");
  assert.equal(dictationSpaced("d", "Next part"), " Next part");
  assert.equal(dictationSpaced(" ", "word"), "word");
  assert.equal(dictationSpaced("\n", "word"), "word");
  assert.equal(dictationSpaced("(", "aside"), "aside");
  assert.equal(dictationSpaced("“", "quoted"), "quoted");
  assert.equal(dictationSpaced("d", ", and more"), ", and more");
  assert.equal(dictationSpaced(".", "Then"), " Then");
  assert.equal(dictationSpaced("￼", "after a chip"), " after a chip");
  assert.equal(dictationSpaced("d", "   "), "");
});

test("live text is revised in place and final text is kept", () => {
  let state = { anchor: 4, pending: 0 };
  const edits = [];
  for (const [text, final, before] of [["hello", false, "d"], ["Hello wor", false, "d"], ["Hello world.", true, "d"], ["Next", false, "."], ["", false, "."]]) {
    const step = dictationStep(state, text, final, before);
    edits.push(plain(step.edit));
    state = step.state;
  }
  assert.deepEqual(edits, [
    { start: 4, end: 4, text: " hello" },
    { start: 4, end: 10, text: " Hello wor" },
    { start: 4, end: 14, text: " Hello world." },
    { start: 17, end: 17, text: " Next" },
    { start: 17, end: 22, text: "" },
  ]);
  assert.deepEqual(plain(state), { anchor: 17, pending: 0 });
  // A selection at the start is replaced by the first result.
  assert.deepEqual(plain(dictationStep({ anchor: 0, pending: 3 }, "new", true, "").edit), { start: 0, end: 3, text: "new" });
});

function nativeHost() {
  const posts = [], replies = [];
  const handler = { postMessage(body) { posts.push(body); return new Promise((resolve, reject) => replies.push({ body, resolve, reject })); } };
  const script = loadClassicScript("dictation.js", { webkit: { messageHandlers: { pharosDictation: handler } }, crypto: { randomUUID } });
  const emit = detail => script.window.dispatchEvent(new script.window.CustomEvent("pharos:dictation", { detail }));
  return { script, window: script.window, posts, replies, emit };
}

test("the host adapter routes each session's events and ends it exactly once", async () => {
  const { window, posts, replies, emit } = nativeHost();
  assert.equal(window.pharosHost.capabilities.dictation.available, true);
  const seen = [];
  const session = window.pharosHost.dictation.start({
    onText: (text, final) => seen.push(`text:${text}:${final}`), onState: state => seen.push(`state:${state}`), onEnd: error => seen.push(`end:${error}`),
  });
  const id = posts[0].session;
  assert.equal(posts[0].action, "start");
  assert.ok(id);
  emit({ session: id, type: "state", state: "listening" });
  emit({ session: "other", type: "text", text: "not mine", final: true });
  emit({ session: id, type: "text", text: "Hi", final: false });
  replies[0].resolve({ started: true });
  await session.started;
  void session.stop();
  assert.deepEqual(plain(posts[1]), { action: "stop", session: id });
  emit({ session: id, type: "text", text: "Hi there.", final: true });
  emit({ session: id, type: "end", error: null });
  emit({ session: id, type: "end", error: "late" });
  emit({ session: id, type: "text", text: "late", final: true });
  assert.deepEqual(seen, ["state:listening", "text:Hi:false", "text:Hi there.:true", "end:null"]);

  // A failed start ends once, whichever of the reply and the event comes first.
  const failures = [];
  const failed = window.pharosHost.dictation.start({ onEnd: error => failures.push(error) });
  const failedID = posts.at(-1).session;
  replies.at(-1).reject(new Error("No microphone access."));
  await assert.rejects(failed.started, /No microphone access/);
  emit({ session: failedID, type: "end", error: "No microphone access." });
  assert.deepEqual(failures, ["No microphone access."]);
});

test("dictation is unavailable outside the Mac app", async () => {
  const script = loadClassicScript("dictation.js", { crypto: { randomUUID } });
  assert.equal(script.window.pharosHost.capabilities.dictation.available, false);
  const ended = [];
  const session = script.window.pharosHost.dictation.start({ onEnd: error => ended.push(error) });
  await assert.rejects(session.started, error => error.code === "unsupported");
  assert.equal(ended.length, 1);
});

// A textarea, its microphone button and the page around them, enough for the
// controller: focus, selection, execCommand edits and input events.
function page({ value = "", caret = value.length } = {}) {
  const host = nativeHost(), { window } = host;
  const listeners = target => { const map = {}; target.addEventListener = (type, fn) => (map[type] ??= []).push(fn); target.removeEventListener = () => {}; target.fire = (type, extra = {}) => { const event = { type, defaultPrevented: false, preventDefault() { event.defaultPrevented = true; }, stopPropagation() {}, ...extra }; for (const fn of map[type] || []) fn(event); return event; }; return target; };
  const field = listeners({
    value, selectionStart: caret, selectionEnd: caret, disabled: false, readOnly: false, isConnected: true,
    getAttribute: () => null, getClientRects: () => [{}], contains: node => node === field,
    focus() { window.document.activeElement = field; },
    setSelectionRange(start, end) { field.selectionStart = start; field.selectionEnd = end; },
  });
  const attributes = {};
  const button = listeners({ hidden: false, disabled: false, dataset: {}, hasAttribute: () => false, setAttribute: (name, value) => { attributes[name] = value; }, attributes,
    focus() { window.document.activeElement = button; } });
  window.document.execCommand = (command, _, text = "") => {
    // Like a browser, editing commands change only the focused field.
    if (window.document.activeElement !== field) return false;
    const { value, selectionStart: start, selectionEnd: end } = field, insert = command === "insertText" ? text : "";
    field.value = value.slice(0, start) + insert + value.slice(end);
    field.selectionStart = field.selectionEnd = start + insert.length;
    field.fire("input");
    return true;
  };
  window.MutationObserver = class { observe() {} disconnect() {} };
  const errors = [];
  const detach = window.pharosDictation.attach({ button, field, onError: message => errors.push(message) });
  return { ...host, field, button, errors, detach };
}

test("the microphone button types at the caret and revises live text", async () => {
  const { window, field, button, posts, replies, emit } = page({ value: "Fix the card" });
  await settle();
  assert.equal(button.hidden, true, "hidden until the Mac says it can transcribe");
  replies[0].resolve({ available: true });
  await settle();
  assert.equal(button.hidden, false);
  assert.equal(button.fire("mousedown").defaultPrevented, true, "pressing the button keeps the caret in the field");
  button.fire("click");
  const id = posts[1].session;
  assert.equal(button.dataset.state, "starting");
  replies[1].resolve({ started: true });
  await settle();
  assert.equal(button.dataset.state, "listening");
  assert.equal(button.attributes["aria-pressed"], "true");
  emit({ session: id, type: "text", text: "on the home", final: false });
  assert.equal(field.value, "Fix the card on the home");
  emit({ session: id, type: "text", text: "on the home page", final: true });
  // A keyboard user Tabs to the button to stop: dictation keeps typing, and
  // focus stays on the button.
  button.focus();
  emit({ session: id, type: "text", text: "now", final: false });
  assert.equal(field.value, "Fix the card on the home page now");
  assert.equal(button.dataset.state, "listening");
  assert.equal(window.document.activeElement, button, "focus returns to the button after each edit");
  assert.equal(posts.length, 2, "focus on the button does not cancel dictation");
  button.fire("click");
  assert.equal(posts[2].action, "stop");
  assert.equal(button.dataset.state, "stopping");
  emit({ session: id, type: "text", text: "now", final: true });
  emit({ session: id, type: "end", error: null });
  assert.equal(field.value, "Fix the card on the home page now");
  assert.equal(button.dataset.state, "idle");
});

test("typing, Escape and failures end dictation without losing text", async () => {
  const typed = page({ value: "Note" });
  typed.replies[0].resolve({ available: true });
  await settle();
  typed.button.fire("click");
  const id = typed.posts[1].session;
  typed.emit({ session: id, type: "text", text: "draft", final: false });
  typed.field.value += "!";
  typed.field.fire("input");
  assert.equal(typed.posts.at(-1).action, "cancel", "the user's own typing ends dictation");
  typed.emit({ session: id, type: "text", text: "draft words", final: true });
  assert.equal(typed.field.value, "Note draft!", "results after the user typed are dropped");
  assert.equal(typed.button.dataset.state, "idle");

  const escaped = page();
  escaped.replies[0].resolve({ available: true });
  await settle();
  escaped.button.fire("click");
  escaped.replies[1].resolve({ started: true });
  await settle();
  const key = escaped.field.fire("keydown", { key: "Escape" });
  assert.equal(key.defaultPrevented, true, "Escape stops dictation instead of closing the dialog");
  assert.equal(escaped.posts.at(-1).action, "stop");

  const denied = page();
  denied.replies[0].resolve({ available: true });
  await settle();
  denied.button.fire("click");
  denied.replies[1].reject(new Error("Pharos doesn't have microphone access."));
  await settle();
  assert.deepEqual(denied.errors, ["Pharos doesn't have microphone access."]);
  assert.equal(denied.button.dataset.state, "idle");
});
