// Shared fixtures for recipe tests: loaded schemas, the server's execution
// contract, and helpers that run a card's destination table or cover metric
// against in-memory rows.
import assert from "node:assert/strict";
import * as core from "@pythia-software/query-table-core";
import { instantiateRecipe, queryRecipes } from "../../../src/curiosity-data";
import { cardCoverQuery } from "../../../src/card-cover";
import activity from "../../../../schemas/activity.schema.json";
import conversationActivity from "../../../../schemas/conversation_activity.schema.json";
import findings from "../../../../schemas/findings.schema.json";
import library from "../../../../schemas/library.schema.json";
import mcpCalls from "../../../../schemas/mcp_calls.schema.json";
import parallelism from "../../../../schemas/parallelism.schema.json";
import skillUsages from "../../../../schemas/skill_usages.schema.json";
import tl1Attempts from "../../../../schemas/tl1_attempts.schema.json";
import toolCalls from "../../../../schemas/tool_calls.schema.json";
import tools from "../../../../schemas/tools.schema.json";
import usage from "../../../../schemas/usage.schema.json";
import writing from "../../../../schemas/writing.schema.json";
import writingMessages from "../../../../schemas/writing_messages.schema.json";
import agentEvents from "../../../../schemas/agent_events.schema.json";
import agentEventMessages from "../../../../schemas/agent_event_messages.schema.json";

export { core, instantiateRecipe, queryRecipes, cardCoverQuery };

// Every dataset the repository defines a schema for.
export const schemas = Object.fromEntries(Object.entries({
  activity, conversation_activity: conversationActivity, findings, library, mcp_calls: mcpCalls, parallelism, skill_usages: skillUsages, tl1_attempts: tl1Attempts, tool_calls: toolCalls, tools, usage, writing, writing_messages: writingMessages, agent_events: agentEvents, agent_event_messages: agentEventMessages,
}).map(([dataset, document]) => [dataset, core.loadSchema(document)]));
export { execution, queryCapabilities as capabilities } from "../../support/query-server.mjs";
export const day = 86400000;

// `npm run test:full` (PHAROS_TEST_FULL=1) runs every combination of the
// combinatorial tests; the default tier runs a representative slice.
export const fullMatrix = process.env.PHAROS_TEST_FULL === "1";

export function recipeById(id) {
  const recipe = queryRecipes.find(recipe => recipe.id === id);
  assert.ok(recipe, `unknown recipe ${id}`);
  return recipe;
}

// Runs cards as of `now`: `card` instantiates a recipe, `table` executes its
// grouped destination and `metric` evaluates the metric its cover shows.
export function recipeHarness(now) {
  const card = (id, days = 7) => { const recipe = recipeById(id); return { recipe, query: instantiateRecipe(recipe, days, new Date(now)).query }; };
  const table = (id, rows, days = 7) => { const { recipe, query } = card(id, days); return core.applyQuery(rows, query, schemas[recipe.dataset], { now }).rows; };
  const metric = (id, rows, days = 7) => {
    const { recipe, query } = card(id, days), cover = cardCoverQuery(query, recipe);
    return core.evaluateMetrics(rows, cover.query, schemas[recipe.dataset], { now }).metrics.find(metric => metric.id === cover.metricId);
  };
  return { card, table, metric };
}
