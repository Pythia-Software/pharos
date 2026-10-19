import test from "node:test";
import assert from "node:assert/strict";
import { loadSchema } from "@pythia-software/query-table-core";
import activity from "../../../../schemas/activity.schema.json";
import conversationActivity from "../../../../schemas/conversation_activity.schema.json";
import library from "../../../../schemas/library.schema.json";
import mcpCalls from "../../../../schemas/mcp_calls.schema.json";
import skillUsages from "../../../../schemas/skill_usages.schema.json";
import tl1Attempts from "../../../../schemas/tl1_attempts.schema.json";
import toolCalls from "../../../../schemas/tool_calls.schema.json";
import tools from "../../../../schemas/tools.schema.json";
import usage from "../../../../schemas/usage.schema.json";
import writing from "../../../../schemas/writing.schema.json";
import writingMessages from "../../../../schemas/writing_messages.schema.json";
import { optionLabels } from "../../../src/option-labels";

const documents = { activity, conversation_activity: conversationActivity, library, mcp_calls: mcpCalls, skill_usages: skillUsages, tl1_attempts: tl1Attempts, tool_calls: toolCalls, tools, usage, writing, writing_messages: writingMessages };
const labels = Object.fromEntries(Object.entries(documents).map(([dataset, document]) => [dataset, optionLabels(loadSchema(document).fields)]));

test("Library source filters label raw source_kind values from the schema", () => {
  const sources = labels.library.get("source_kind");
  assert.equal(sources.get("chatgpt"), "ChatGPT");
  assert.equal(sources.get("tl1-export"), "TL1 export");
  const declared = library.fields.find(field => field.name === "source_kind").filter.values.options;
  assert.deepEqual([...sources.keys()], declared.map(option => option.value), "every source keeps its raw value as the key");
});

test("static options have unique values and distinct labels", () => {
  for (const [dataset, document] of Object.entries(documents)) for (const field of document.fields) {
    const values = field.filter?.values;
    if (values?.source !== "static") continue;
    const options = values.options.map(option => typeof option === "string" ? { value: option } : option);
    assert.equal(new Set(options.map(option => option.value)).size, options.length, `${dataset}.${field.name} values`);
    const labelled = options.filter(option => option.label !== undefined);
    assert.ok(labelled.every(option => option.label.trim()), `${dataset}.${field.name} labels are not blank`);
    assert.equal(new Set(labelled.map(option => option.label)).size, labelled.length, `${dataset}.${field.name} labels are distinct`);
  }
});

test("a value shared by several datasets has the same label everywhere", () => {
  const seen = new Map();
  for (const [dataset, fields] of Object.entries(labels)) for (const [field, options] of fields) for (const [value, label] of options) {
    const key = `${field}=${value}`, first = seen.get(key);
    if (first) assert.equal(label, first.label, `${key}: ${dataset} and ${first.dataset}`);
    else seen.set(key, { dataset, label });
  }
  for (const field of ["source_kind", "provider"])
    assert.ok(Object.values(labels).filter(fields => fields.has(field)).length > 1, `${field} is shared`);
});

test("only labelled static options are listed", () => {
  const fields = [
    { name: "plain", type: "enum", filter: { values: { source: "static", options: ["a", "b"] } } },
    { name: "mixed", type: "enum", filter: { values: { source: "static", options: ["a", { value: "b", label: "Bee" }] } } },
    { name: "remote", type: "enum", filter: { values: { source: "distinct" } } },
    { name: "free", type: "text" },
  ];
  assert.deepEqual([...optionLabels(fields)].map(([field, options]) => [field, [...options]]), [["mixed", [["b", "Bee"]]]]);
});
