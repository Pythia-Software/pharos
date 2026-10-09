import assert from "node:assert/strict";
import test from "node:test";
import { build } from "esbuild";

const compiled = await build({ entryPoints: ["src/computed-column-store.ts"], bundle: true, write: false, platform: "node", format: "esm" });
const { withComputedCapabilityRefresh } = await import(`data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].text).toString("base64")}`);
const column = { id: "seconds", label: "Seconds", expression: { language: "qt-expr", version: 1, source: "[duration_ms]/1000" } };

for (const revision of [null, "1"]) {
  test(`save refreshes capabilities before publishing a ${revision === null ? "new" : "changed"} definition`, async () => {
    const events = [];
    const saved = { ...column, revision: revision === null ? "1" : "2" };
    let finishRefresh;
    const refreshed = new Promise(resolve => { finishRefresh = resolve; });
    const store = withComputedCapabilityRefresh({
      async list() { return []; },
      async save(dataset, draft, expected) {
        assert.equal(dataset, "pharos_tool_calls");
        assert.equal(draft, column);
        assert.equal(expected, revision);
        events.push("saved");
        return saved;
      },
    }, async () => { events.push("refreshing"); await refreshed; events.push("capabilities published"); });
    const pending = store.save("pharos_tool_calls", column, revision).then(value => { events.push("definition published"); return value; });
    await new Promise(resolve => setImmediate(resolve));
    assert.deepEqual(events, ["saved", "refreshing"]);
    finishRefresh();
    assert.equal(await pending, saved);
    assert.deepEqual(events, ["saved", "refreshing", "capabilities published", "definition published"]);
  });
}

test("list forwards cancellation and retains subscription support", async () => {
  const signal = new AbortController().signal;
  const definitions = [{ ...column, revision: "1" }];
  const subscribe = () => () => {};
  let refreshed = false;
  const store = withComputedCapabilityRefresh({
    subscribe,
    async list(dataset, receivedSignal) { assert.equal(dataset, "pharos_tool_calls"); assert.equal(receivedSignal, signal); return definitions; },
    async save() { throw new Error("unexpected save"); },
  }, async receivedSignal => { assert.equal(receivedSignal, signal); refreshed = true; });
  assert.equal(await store.list("pharos_tool_calls", signal), definitions);
  assert.equal(refreshed, true);
  assert.equal(store.subscribe, subscribe);
});

test("failed saves do not publish capabilities or a definition", async () => {
  const store = withComputedCapabilityRefresh({
    async list() { return []; },
    async save() { throw new Error("revision conflict"); },
  }, async () => { assert.fail("failed save refreshed capabilities"); });
  await assert.rejects(store.save("pharos_tool_calls", column, "1"), /revision conflict/);
});
