import assert from "node:assert/strict";
import test from "node:test";
import { applyQuery, loadSchema, EMPTY_QUERY } from "@pythia-software/query-table-core";
import toolCalls from "../../../../schemas/tool_calls.schema.json";
import { machineWindow, machineDay, machineWhere, activityFor, timeFacts, totalTime, groupTime, relativeChange, exampleTimeFacts, exampleConcurrency, noRepository, operationKey, operationName, activityPresentation, timeValue, factsFromCalls, exampleTimeCalls, previousTimeWhere, timeAggregationWhere, machineDrilldownWhere, selectedMachineRepository } from "../../../src/machine-activity-data";
process.env.TZ = "America/Denver";
const scope = { days: 7, repository: "repo-a", provider: "codex", model: "example-model" };
const window = machineWindow(7, new Date("2026-03-10T19:25:00-06:00"));

test("periods use complete local days and an adjacent calendar comparison across DST", () => {
  assert.deepEqual(window, { start: "2026-03-03T07:00:00.000Z", end: "2026-03-10T06:00:00.000Z", previous: "2026-02-24T07:00:00.000Z" });
  const current = machineWhere(scope, window);
  const previous = machineWhere(scope, window, "started_at", true);
  assert.deepEqual(current.slice(0, 3), previous.slice(0, 3));
  assert.equal(current.at(-2).value, previous.at(-1).value);
  assert.equal(machineWhere(scope, window, "last_usage_at").at(-1).field, "last_usage_at");
  assert.equal(machineWhere({ ...scope, days: 0 }, window).length, 3);
});

test("missing timing is excluded from averages while recorded zero-duration calls count", () => {
  const keys = ["repo-a", "test", "2026-03-05", "ok", "go-test", "go test", "Bash", ""];
  const errorKeys = ["repo-a", "test", "2026-03-05", "error", "go-test", "go test", "Bash", ""];
  const missingKeys = [null, "edit", "2026-03-05", "ok", "edit", "Edit", "Edit", ""];
  const facts = timeFacts([
    { id: "time", buckets: [{ keys, value: 120000, count: 4 }, { keys: errorKeys, value: 60000, count: 1 }, { keys: missingKeys, value: null, count: 2 }] },
    { id: "timed", buckets: [{ keys, value: 3, count: 4 }, { keys: errorKeys, value: 1, count: 1 }, { keys: missingKeys, value: 0, count: 2 }] },
    { id: "population", buckets: [{ keys: [], value: 7 }] },
  ]);
  const total = totalTime(facts);
  assert.deepEqual(total, { duration: 180000, calls: 7, timed: 4, failedDuration: 60000 });
  assert.equal(total.duration / total.timed, 45000);
  assert.equal(facts[2].repository, noRepository);
  assert.equal(groupTime(facts, fact => fact.activity)[0].key, "test");
  assert.equal(machineWhere({ ...scope, repository: noRepository, operationKey, operationName, activityPresentation, timeValue, factsFromCalls, exampleTimeCalls, previousTimeWhere }, window)[0].any[0].op, "is_null");
});

test("activity is based on recorded categories and never invents preflight or reasoning time", () => {
  assert.equal(activityFor("command", "test"), "test");
  assert.equal(activityFor("command", "package"), "setup");
  assert.equal(activityFor("edit", ""), "edit");
  assert.equal(activityFor("command", "file-write"), "edit");
  assert.equal(activityFor("web", ""), "read");
  assert.equal(activityFor("agent", ""), "delegate");
  assert.equal(activityFor("unknown", "preflight", "special_tool"), "tool:special_tool");
  assert.equal(relativeChange(100, 0), "New");
  assert.equal(relativeChange(0, 100), "−100%");
  assert.equal(relativeChange(0, 0), "—");
});

test("examples obey every filter and comparison dates do not overlap", () => {
  const exampleScope = { ...scope, repository: "pharos", model: "gpt-5.3-codex" };
  const current = exampleTimeFacts(exampleScope, window);
  const previous = exampleTimeFacts(exampleScope, window, true);
  assert.ok(current.length);
  assert.ok(current.every(fact => fact.repository === "pharos"));
  assert.ok(current.every(fact => fact.day >= machineDay(window.start) && fact.day < machineDay(window.end)));
  assert.ok(previous.every(fact => fact.day < machineDay(window.start)));
  assert.deepEqual(exampleTimeFacts({ ...exampleScope, provider: "claude" }, window), []);
  assert.equal(exampleConcurrency(window, 7).days.length, 7);
  assert.ok(exampleConcurrency(window, 7).threads.every(thread => thread.turns.every(([start, end]) => end > start)));
});


test("specific identities retain named tasks and MCP server boundaries", () => {
  assert.equal(operationKey("Bash", "", "npm", "run build"), operationKey("exec_command", "", "npm", "run build"));
  assert.notEqual(operationKey("Bash", "", "npm", "run test:unit"), operationKey("Bash", "", "npm", "run test:integration"));
  assert.notEqual(operationKey("search", "github", "", ""), operationKey("search", "linear", "", ""));
  assert.notEqual(operationKey("custom_a", "", "", ""), operationKey("custom_b", "", "", ""));
  assert.equal(operationName("search", "github", "", ""), "github / search");
  assert.equal(activityPresentation(activityFor("mcp", "", "search", "github")).label, "github / search");
});

test("per-run trends weight calls and leave missing means empty", () => {
  const a = { duration: 60000, calls: 1, timed: 1, failedDuration: 0 };
  const b = { duration: 900000, calls: 9, timed: 9, failedDuration: 0 };
  assert.equal(timeValue(totalTime([a,b]), "average"), 96000);
  assert.equal(timeValue({ ...a, calls: 100 }, "average"), 60000);
  assert.equal(timeValue(totalTime([]), "average"), null);
  assert.equal(timeValue({ ...a, duration: 0 }, "average"), 0);
  assert.equal(timeValue({ ...a, calls: 100 }, "calls"), 100);
});

test("truncated group sets are rejected before totals or means are shown", () => {
  const key = ["repo-a", "test", "2026-03-05", "ok", "go-test", "go test", "Bash", ""];
  assert.throws(() => timeFacts([{id:"time", buckets:[{keys:key,value:60000,count:1}]}, {id:"timed", buckets:[{keys:key,value:1,count:1}]}, {id:"population", buckets:[{keys:[],value:2}]}]), /incomplete/);
});

test("raw examples reconcile with analytics and distinguish slowdown from volume", () => {
  const scope = { days: 7, repository: "pharos", provider: "", model: "" };
  const rows = exampleTimeCalls(scope, window);
  const facts = factsFromCalls(rows, "day");
  assert.equal(totalTime(facts).calls, rows.length);
  assert.equal(totalTime(facts).duration, rows.reduce((sum, row) => sum + (row.duration_ms ?? 0), 0));
  const buildKey = operationKey("exec_command", "", "npm", "run build");
  const builds = facts.filter(fact => fact.operation === buildKey);
  const prior = exampleTimeFacts(scope, window, true).filter(fact => fact.operation === buildKey);
  assert.equal(totalTime(builds).calls, totalTime(prior).calls);
  assert.ok(timeValue(totalTime(builds), "average") > timeValue(totalTime(prior), "average"));
  const priorWhere = previousTimeWhere([...machineWhere(scope, window), {field:"operation_key",op:"=",value:buildKey},{field:"status",op:"=",value:"ok"}], 7);
  assert.equal(priorWhere.at(-2).value, buildKey);
  assert.equal(priorWhere.at(-1).value, "ok");
  assert.equal(priorWhere[1].value, window.previous);
});


test("complete local-day bounds use equivalent cube filters across DST, while exact times stay exact", () => {
  const current = machineWhere(scope, window);
  assert.deepEqual(timeAggregationWhere(current).at(-1), {any:Array.from({length:7},(_,index)=>({field:"day",op:"=",value:`2026-03-${String(index+3).padStart(2,"0")}`}))});
  const untouched = [{field:"started_at",op:">=",value:"2026-03-04T15:30:00.000Z"}, {field:"started_at",op:"<=",value:window.end}, {field:"ended_at",op:"<",value:window.end}];
  assert.deepEqual(timeAggregationWhere(untouched), untouched);
  assert.deepEqual(timeAggregationWhere([{field:"started_at",op:">=",value:window.start,negated:true},{field:"started_at",op:"<",value:window.end}]), [{field:"started_at",op:">=",value:window.start,negated:true},{field:"started_at",op:"<",value:window.end}]);
  const rows = exampleTimeCalls({...scope,repository:"",provider:"",model:""},window);
  const lower = new Date(window.start).getTime(), upper = new Date(window.end).getTime();
  const raw = rows.filter(row => new Date(row.started_at).getTime() >= lower && new Date(row.started_at).getTime() < upper);
  const day = rows.filter(row => row.day >= "2026-03-03" && row.day < "2026-03-10");
  assert.deepEqual(raw.map(row=>row.id),day.map(row=>row.id));
});


const callSchema = loadSchema(toolCalls);
const repositoryRows = ["repo-a", "repo-b", null, ""].map((repository_name, index) => ({ id: String(index), repository_name, activity: "test", status: "ok" }));
const repositoryFilter = repository => machineWhere({ days: 0, repository, provider: "", model: "" }, window);
const matchingRepositories = where => applyQuery(repositoryRows, { ...EMPTY_QUERY, where }, callSchema).rows.map(row => row.id);

test("missing repository replaces a named selection and can be replaced by another repository", () => {
  const missing = machineDrilldownWhere(repositoryFilter("repo-a"), repositoryFilter(noRepository));
  assert.deepEqual(matchingRepositories(missing), ["2", "3"]);
  assert.equal(selectedMachineRepository(missing, ""), noRepository);
  const named = machineDrilldownWhere(missing, repositoryFilter("repo-b"));
  assert.deepEqual(matchingRepositories(named), ["1"]);
  assert.equal(selectedMachineRepository(named, ""), "repo-b");
});

test("missing repository toggles off after URL normalization and removes existing duplicate selections", () => {
  const missing = repositoryFilter(noRepository)[0];
  const normalized = { any: [...missing.any].reverse().map(clause => ({ ...clause, negated: false })) };
  const where = [missing, normalized, { field: "status", op: "=", value: "ok" }];
  const cleared = machineDrilldownWhere(where, [normalized], true);
  assert.deepEqual(matchingRepositories(cleared), ["0", "1", "2", "3"]);
  assert.equal(selectedMachineRepository(cleared, ""), "");
  assert.deepEqual(cleared, [{ field: "status", op: "=", value: "ok" }]);
});

test("repeated missing-repository segments replace their filters and preserve mixed-field ORs", () => {
  const mixed = { any: [{ field: "repository_name", op: "=", value: "repo-a" }, { field: "status", op: "=", value: "ok" }] };
  let where = [mixed, ...repositoryFilter("repo-a")];
  const segment = [...repositoryFilter(noRepository), { field: "activity", op: "=", value: "test" }];
  for (let click = 0; click < 3; click++) where = machineDrilldownWhere(where, segment);
  assert.deepEqual(matchingRepositories(where), ["2", "3"]);
  assert.equal(where.filter(term => "any" in term).length, 2, "One repository OR plus the unrelated mixed-field OR");
  assert.ok(where.includes(mixed));
});
