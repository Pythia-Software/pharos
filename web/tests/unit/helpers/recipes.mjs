// Shared fixtures for recipe tests: loaded schemas and the server's execution
// contract.
import * as core from "@pythia-software/query-table-core";
import { instantiateRecipe, queryRecipes } from "../../../src/curiosity-data";
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

export { core, instantiateRecipe, queryRecipes };

// Every dataset the repository defines a schema for.
export const schemas = Object.fromEntries(Object.entries({
  activity, conversation_activity: conversationActivity, findings, library, mcp_calls: mcpCalls, parallelism, skill_usages: skillUsages, tl1_attempts: tl1Attempts, tool_calls: toolCalls, tools, usage, writing, writing_messages: writingMessages,
}).map(([dataset, document]) => [dataset, core.loadSchema(document)]));
export const execution = { profile: "qt-sqlite-v1", planToken: "", fields: {}, resolvedRevisions: {} };
export const capabilities = { version: 3, profile: execution.profile, groupedRows: true, calendarGrouping: true, computedSourceFilters: true, regex: false, maxRows: 10000, maxGroups: 10000, maxPopulation: 250000 };
export const day = 86400000;

// `npm run test:full` (PHAROS_TEST_FULL=1) runs every combination of the
// combinatorial tests; the default tier runs a representative slice.
export const fullMatrix = process.env.PHAROS_TEST_FULL === "1";
