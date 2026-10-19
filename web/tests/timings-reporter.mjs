// A node:test reporter that records each finished test's duration as JSON
// lines; run.mjs reads them to list the slowest tests.
export default async function* timings(source) {
  for await (const event of source) {
    if (event.type !== "test:pass" && event.type !== "test:fail") continue;
    const { name, nesting, file, details, skip, todo } = event.data;
    if (skip || todo || details?.type === "suite" || (file && name === file)) continue;
    yield `${JSON.stringify({ name, nesting, file, duration: details?.duration_ms ?? 0 })}\n`;
  }
}
