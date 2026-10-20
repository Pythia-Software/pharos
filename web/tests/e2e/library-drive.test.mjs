// End to end: a freshly built pharos serving a synthetic portable library on
// a disk image this test creates and detaches. Two Macs are simulated with
// PHAROS_HOST_ID: "MacBook Air" captures first from the CLI; "Mac Studio" is
// the Mac running the service, onboarding for the first time. Nothing outside
// the temporary directory and the disk image is written: HOME and
// PHAROS_SUPPORT_DIR point into the temporary directory for every process.
//
//   npm --prefix web run test:e2e      (sets PHAROS_E2E=1; macOS only)
//
// The cases run in order: the second expects the Mac the first onboarded.
// PHAROS_UI_SCREENSHOTS=DIR saves screenshots (e2e-*.png) there.
import { after, before, describe, it } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { connectBrowser, launchPlan } from "../browser/engine.mjs";

const root = fileURLToPath(new URL("../../..", import.meta.url));
const skip = process.env.PHAROS_E2E !== "1" ? "set PHAROS_E2E=1 (npm --prefix web run test:e2e)"
  : process.platform !== "darwin" ? "needs macOS (hdiutil)" : launchPlan().skip ?? false;
const screenshots = process.env.PHAROS_UI_SCREENSHOTS;

let work, volume, mount, device, library, token, base, service, browser, binary;
const hostA = { id: "host-a", label: "Mac Studio" }, hostB = { id: "host-b", label: "MacBook Air" };
const envFor = host => ({ ...process.env, HOME: host.home, PHAROS_SUPPORT_DIR: host.support, PHAROS_HOST_ID: host.id, PHAROS_HOST_LABEL: host.label, PHAROS_PROBE_SHELL: "0" });
const cli = (host, ...args) => execFileSync(binary, ["--config", join(library, "library.toml"), ...args], { env: envFor(host), encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });

function write(file, text) {
  mkdirSync(dirname(file), { recursive: true });
  writeFileSync(file, text);
}
function claudeSession(home, project, session, turns) {
  const lines = [];
  for (let i = 0; i < turns; i++) {
    const time = `2026-09-0${1 + (i % 9)}T10:${String(i).padStart(2, "0")}:00Z`;
    lines.push(JSON.stringify({ type: "user", uuid: `${session}-u${i}`, sessionId: session, cwd: `/Users/test/${project}`, timestamp: time, message: { role: "user", content: `Question ${i} about ${project}` } }));
    lines.push(JSON.stringify({ type: "assistant", uuid: `${session}-a${i}`, parentUuid: `${session}-u${i}`, sessionId: session, cwd: `/Users/test/${project}`, timestamp: time, message: { role: "assistant", model: "claude-test", content: [{ type: "text", text: `Answer ${i} for ${project}. `.repeat(20) }] } }));
  }
  write(join(home, ".claude/projects", `-Users-test-${project}`, `${session}.jsonl`), lines.join("\n") + "\n");
}
function codexSession(home, id, messages) {
  const lines = [JSON.stringify({ type: "session_meta", timestamp: "2026-09-02T09:00:00Z", payload: { id, cwd: "/Users/test/codex-project" } })];
  for (let i = 0; i < messages; i++) lines.push(JSON.stringify({ type: "event_msg", timestamp: `2026-09-02T09:${String(i + 1).padStart(2, "0")}:00Z`, payload: { type: "user_message", message: `codex message ${i}` } }));
  write(join(home, ".codex/sessions/2026/09/02", `rollout-2026-09-02T09-00-00-${id}.jsonl`), lines.join("\n") + "\n");
}
const freePort = () => new Promise((resolve, reject) => {
  const server = createServer();
  server.listen(0, "127.0.0.1", () => { const { port } = server.address(); server.close(() => resolve(port)); });
  server.on("error", reject);
});
// Polls the service, which has no event to wait on.
async function eventually(check, timeout) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const value = await check().catch(() => null);
    if (value) return value;
    if (Date.now() > deadline) throw new Error("timed out");
    await new Promise(resolve => setTimeout(resolve, 250));
  }
}
const api = async (path, options = {}) => (await fetch(`${base}${path}`, { ...options, headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" } })).json();
async function shoot(target, name) {
  if (!screenshots) return;
  mkdirSync(screenshots, { recursive: true });
  await target.screenshot({ path: join(screenshots, `e2e-${name}.png`) });
}

async function openPage(t, { onboarding = false } = {}) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  t.after(() => context.close());
  // Only the onboarding case sees onboarding.
  if (!onboarding) await context.addInitScript(id => sessionStorage.setItem(`pharos-onboarding-dismissed:${id}`, "1"), hostA.id);
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  t.after(() => assert.deepEqual(errors, [], "page errors"));
  await page.goto(`${base}/?token=${encodeURIComponent(token)}`);
  return page;
}

describe("library drive, end to end", { skip }, () => {
  before(async () => {
    work = mkdtempSync(join(tmpdir(), "pharos-e2e-"));
    volume = `PharosE2E-${process.pid}`;
    binary = join(work, "pharos");
    Object.assign(hostA, { home: join(work, "home-a"), support: join(work, "support-a") });
    Object.assign(hostB, { home: join(work, "home-b"), support: join(work, "support-b") });
    execFileSync("go", ["build", "-o", binary, "./cmd/pharos"], { cwd: root, env: { ...process.env, GOTOOLCHAIN: "auto" } });
    // Sparse, so it takes only what is written; captures want 1 GB free.
    execFileSync("hdiutil", ["create", "-quiet", "-type", "SPARSE", "-size", "3g", "-fs", "APFS", "-volname", volume, join(work, "library")]);
    const attached = execFileSync("hdiutil", ["attach", "-nobrowse", join(work, "library.sparseimage")], { encoding: "utf8" });
    device = attached.trim().split("\n")[0].split(/\s+/)[0];
    mount = attached.split("\n").map(line => line.split("\t").pop().trim()).find(value => value.startsWith("/Volumes/"));
    assert.ok(mount?.startsWith(`/Volumes/${volume}`), `mounted at ${mount}`);
    library = join(mount, "Pharos");
    execFileSync(binary, ["init-library", library], { env: envFor(hostA), stdio: "ignore" });
    const port = await freePort();
    const config = join(library, "library.toml");
    writeFileSync(config, readFileSync(config, "utf8").replace(/^port = \d+$/m, `port = ${port}`));
    token = readFileSync(config, "utf8").match(/^api_token = "(.*)"$/m)[1];
    base = `http://127.0.0.1:${port}`;

    // MacBook Air: sources found by the probe and captured from the CLI, not
    // indexed. Enough sessions that the index shows progress.
    for (let i = 0; i < 120; i++) claudeSession(hostB.home, `air-${i % 40}`, `b${String(i).padStart(7, "0")}-0000-4000-8000-000000000000`, 3);
    cli(hostB, "probe", "--accept-all");
    cli(hostB, "capture");
    // Mac Studio: found by onboarding in the browser.
    claudeSession(hostA.home, "studio-app", "a0000000-0000-4000-8000-000000000001", 6);
    claudeSession(hostA.home, "studio-app", "a0000000-0000-4000-8000-000000000002", 5);
    claudeSession(hostA.home, "studio-docs", "a0000000-0000-4000-8000-000000000003", 2);
    codexSession(hostA.home, "c0000000-0000-4000-8000-000000000001", 5);

    service = spawn(binary, ["--config", config, "serve"], { env: envFor(hostA), stdio: ["ignore", "ignore", "pipe"] });
    let stderr = "";
    service.stderr.on("data", chunk => { stderr += chunk; });
    await eventually(async () => (await fetch(`${base}/api/health`, { headers: { Authorization: `Bearer ${token}` } })).ok, 60000).catch(() => { throw new Error(`service did not start: ${stderr}`); });
    browser = await connectBrowser();
  }, { timeout: 900000 });

  after(async () => {
    await browser?.close();
    if (service && service.exitCode === null) {
      service.kill("SIGTERM");
      await new Promise(resolve => { service.once("exit", resolve); setTimeout(resolve, 10000); });
    }
    if (device) { try { execFileSync("hdiutil", ["detach", "-force", device], { stdio: "ignore" }); } catch {} }
    if (work) rmSync(work, { recursive: true, force: true });
  }, { timeout: 60000 });

  it("onboards a new Mac: add sources, capture, then index", { timeout: 300000 }, async t => {
    const page = await openPage(t, { onboarding: true });
    const dialog = page.locator("#pharosOnboarding [role=dialog]");
    await dialog.waitFor();
    await dialog.getByText("~/.claude/projects").waitFor();
    await dialog.getByRole("button", { name: /^Add \d+ sources?$/ }).click();
    await dialog.getByRole("button", { name: "Index now" }).waitFor({ timeout: 60000 });
    await shoot(page, "onboarding-captured");
    await dialog.getByRole("button", { name: "Index now" }).click();
    await dialog.getByRole("button", { name: "Done" }).waitFor({ timeout: 120000 });
    await shoot(page, "onboarding-indexed");
    const studio = (await api("/api/index")).hosts.find(host => host.id === hostA.id);
    assert.deepEqual(studio.sources.map(source => [source.name, source.needs_index]).sort(), [["claude", false], ["codex", false]]);
    await dialog.getByRole("button", { name: "Done" }).click();
    await page.locator("#pharosOnboarding").waitFor({ state: "detached" });
  });

  it("shows another Mac's captures as needing an index, and indexes them", { timeout: 420000 }, async t => {
    const page = await openPage(t);
    await page.goto(`${base}/settings/sources`);
    const air = page.locator(`#sourceGrid .source-card.remote[data-host="${hostB.id}"][data-source="claude"]`);
    await air.waitFor();
    assert.match(await air.textContent(), /needs index/);
    await shoot(page.locator("#sourceGrid"), "hosts-needs-index");
    await page.goto(`${base}/settings/sync`);
    await page.locator("#syncOperations summary").click();
    await page.locator("#syncRunActions").getByRole("button", { name: "Index captured files" }).click();
    // The index runs in the service; the page shows it finish.
    await eventually(async () => (await api("/api/index")).hosts.find(host => host.id === hostB.id)?.sources.every(source => !source.needs_index), 300000);
    await page.goto(`${base}/settings/sources`);
    await air.waitFor();
    await page.waitForFunction(selector => !/needs index/.test(document.querySelector(selector)?.textContent ?? "needs index"), `#sourceGrid .source-card.remote[data-host="${hostB.id}"][data-source="claude"]`, { timeout: 30000 });
    await shoot(page.locator("#sourceGrid"), "hosts-indexed");
    const remote = (await api("/api/index")).hosts.find(host => host.id === hostB.id).sources[0];
    assert.equal(remote.coverage, "complete");
    assert.ok(remote.last_attempt_at);
  });
});
