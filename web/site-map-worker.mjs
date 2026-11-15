// A worker for generate-site-map.mjs: works out one dashboard view at a time
// (viewFacts) from the generator's bundle, with the cases it picks from the
// combinations it enumerated, and posts both back.
import { parentPort, workerData } from "node:worker_threads";
// The generator renders in UTC; a worker keeps the environment it started with.
process.env.TZ = "UTC";
globalThis.location = { origin: "http://localhost", search: "", pathname: "/" };
const { viewFacts, viewCasePicks } = await import(workerData.entry);
parentPort.on("message", ({ topic, view, label }) => {
  try {
    const { facts, states } = viewFacts(topic, view, label);
    parentPort.postMessage({ topic, view, facts, picks: viewCasePicks(topic, view, states) });
  } catch (error) {
    parentPort.postMessage({ topic, view, error: error?.stack ?? String(error) });
  }
});
