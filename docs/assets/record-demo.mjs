// Records the Mercator console demo (docs/assets/mercator-demo.webm) with
// Playwright. It seeds a run through the API if the workspace is empty, then
// walks the console: runs list -> run detail -> placement decision -> events.
//
// Prerequisites: a running Docker daemon, the broker up on $BASE (see the
// README "Try It In 5 Minutes" quickstart), and Playwright available:
//
//   npm i playwright && npx playwright install chromium
//   node docs/assets/record-demo.mjs
//   # then convert to the GIF fallback (see docs/assets/README.md) and move
//   # both files into docs/assets/.
//
// The token and workspace are seeded into localStorage (never the URL), exactly
// as the console itself stores them, so nothing sensitive is captured. The run
// is short-lived busybox `echo` — no private image, host path, or credential.

import { chromium } from "playwright";
import { execSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";

const BASE = process.env.BASE || "http://127.0.0.1:8080";
const TOKEN = process.env.MERCATOR_API_TOKEN || "dev-token";
const WS = process.env.MERCATOR_WORKSPACE_ID || "ws_1";
const OUT = process.env.OUT || "docs/assets/output";
const W = 1280, H = 800;

const auth = { Authorization: `Bearer ${TOKEN}`, "Content-Type": "application/json" };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function listRuns() {
  const res = await fetch(`${BASE}/v1/runs?workspace_id=${WS}`, { headers: auth });
  if (!res.ok) throw new Error(`GET /v1/runs -> ${res.status}`);
  return (await res.json()).runs ?? [];
}

// Seed one digest-pinned busybox run if the workspace has none. The Docker
// adapter rejects mutable tags, so resolve a digest from the local daemon.
async function seedIfEmpty() {
  if ((await listRuns()).length > 0) return;
  execSync("docker pull -q busybox:latest", { stdio: "ignore" });
  const image = execSync(
    "docker inspect --format '{{index .RepoDigests 0}}' busybox:latest",
  ).toString().trim();
  const res = await fetch(`${BASE}/v1/runs?workspace_id=${WS}`, {
    method: "POST",
    headers: { ...auth, "Idempotency-Key": "console-demo-seed" },
    body: JSON.stringify({ image, args: ["echo", "hi"] }),
  });
  if (!res.ok) throw new Error(`seed run -> ${res.status}: ${await res.text()}`);
}

await seedIfEmpty();

fs.rmSync(OUT, { recursive: true, force: true });
fs.mkdirSync(OUT, { recursive: true });

const browser = await chromium.launch({
  headless: true,
  args: ["--force-color-profile=srgb", "--hide-scrollbars", "--disable-dev-shm-usage"],
});
const context = await browser.newContext({
  viewport: { width: W, height: H },
  deviceScaleFactor: 1,
  colorScheme: "dark",
  recordVideo: { dir: OUT, size: { width: W, height: H } },
});
await context.addInitScript(
  ([token, ws]) => {
    localStorage.setItem("mercator.token", token);
    localStorage.setItem("mercator.workspace", ws);
    localStorage.setItem("mercator.recentWorkspaces", JSON.stringify([ws]));
  },
  [TOKEN, WS],
);

const page = await context.newPage();

// 1. Runs list.
await page.goto(`${BASE}/runs?workspace_id=${WS}`, { waitUntil: "networkidle" });
await page.getByText("succeeded", { exact: false }).first().waitFor({ timeout: 15000 });
await sleep(2200);

// 2. Open a run -> detail (phase timeline + run facts).
await page.locator("tbody tr").first().click();
await page.getByText("Outcome", { exact: false }).first().waitFor({ timeout: 15000 });
await sleep(600);

// 3. Decision tab (default): the selected offer and why it won.
await page.getByText("Selected offer", { exact: false }).first().waitFor({ timeout: 15000 });
await page.getByText("offer_docker_loopback", { exact: false }).first().waitFor({ timeout: 15000 });
await sleep(3000);

// 4. Events tab: the public, event-sourced audit trail.
await page.getByRole("tab", { name: "Events" }).click();
await sleep(2800);

await context.close();
await browser.close();

const files = fs.readdirSync(OUT).filter((f) => f.endsWith(".webm"));
if (files.length !== 1) {
  console.error("expected exactly one video, got:", files);
  process.exit(1);
}
fs.renameSync(path.join(OUT, files[0]), path.join(OUT, "mercator-demo.webm"));
console.log("wrote", path.join(OUT, "mercator-demo.webm"));
