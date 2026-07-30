import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";

import { chromium } from "playwright";

function requiredEnv(name) {
  const value = process.env[name]?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}

const baseURL = requiredEnv("MERCATOR_BROWSER_BASE_URL").replace(/\/$/, "");
const outputDirectory = path.resolve(requiredEnv("MERCATOR_BROWSER_OUTPUT"));
const sidecarPath = path.resolve(requiredEnv("MERCATOR_BROWSER_UI_SIDECAR"));
const token = requiredEnv("MERCATOR_LAB_TOKEN");
const sidecar = JSON.parse(await fs.readFile(sidecarPath, "utf8"));
const checkpoints = new Map(
  sidecar.checkpoints.map((checkpoint) => [checkpoint.id, checkpoint]),
);
await fs.rm(outputDirectory, { recursive: true, force: true });
await fs.mkdir(outputDirectory, { recursive: true });

const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({
  viewport: { width: 1440, height: 960 },
  deviceScaleFactor: 1,
  colorScheme: "dark",
});
await context.tracing.start({ screenshots: true, snapshots: true, sources: true });
let page;
let failure;

try {
  await drive({ kind: "step" });
  await context.addInitScript(() => {
    localStorage.setItem("mercator.workspace", "ws_lab");
    localStorage.setItem("mercator.recentWorkspaces", '["ws_lab"]');
  });
  page = await context.newPage();
  page.setDefaultTimeout(15_000);
  const browserFailures = [];
  page.on("console", (message) => {
    if (message.type() === "error") browserFailures.push(message.text());
  });
  page.on("pageerror", (error) => browserFailures.push(error.message));
  page.on("response", (response) => {
    if (response.status() >= 400) {
      browserFailures.push(
        `${response.request().method()} ${response.url()}: ${response.status()}`,
      );
    }
  });

  await page.goto(`${baseURL}/canvas?workspace_id=ws_lab`, {
    waitUntil: "domcontentloaded",
  });
  await page.getByRole("heading", { name: "Workspace", exact: true }).waitFor();
  await page.getByLabel("Workspace events live").waitFor();
  await assertCheckpoint("producer-placement-visible");

  await drive({ kind: "step" });
  await drive({ kind: "advance", duration: "30m" });
  await page
    .locator("li")
    .filter({ hasText: "Booking decided" })
    .filter({ hasText: "runs/run-consumer" })
    .waitFor();
  await assertCheckpoint("consumer-artifact-locality-visible");

  await restart();
  await drive({ kind: "advance", duration: "30m" });
  await page
    .locator("li")
    .filter({ hasText: "Closed" })
    .filter({ hasText: "runs/run-consumer" })
    .waitFor();
  await assertCheckpoint("terminal-lifecycle-visible");

  const runs = await normalJSON("/v1/runs?workspace_id=ws_lab");
  assert.deepEqual(
    runs.runs.map((run) => [run.id, run.outcome]).sort(),
    [
      ["run-consumer", "succeeded"],
      ["run-producer", "succeeded"],
    ],
  );
  const semanticTree = await page.locator("body").ariaSnapshot();
  assert.match(semanticTree, /heading "Workspace"/);
  assert.match(semanticTree, /region "Workspace events"/);
  assert.equal(await page.locator("button:not(:disabled)").count() > 0, true);
  assert.deepEqual(browserFailures, []);
} catch (error) {
  failure = error;
  if (page) {
    await page.screenshot({
      path: path.join(outputDirectory, "failure.png"),
      fullPage: true,
    });
  }
} finally {
  await retainFailure(() =>
    context.tracing.stop({
      path: path.join(outputDirectory, "trace.zip"),
    }),
  );
  await retainFailure(uploadEvidence);
  await retainFailure(saveBundle);
  await retainFailure(() => context.close());
  await retainFailure(() => browser.close());
}

if (failure) {
  failure.message += `\nReplay: mercator lab replay --bundle ${path.join(outputDirectory, "artifact-warmth-restart.mlab")}`;
  throw failure;
}
console.log(`Lab browser proof written to ${outputDirectory}`);

async function assertCheckpoint(id) {
  const checkpoint = checkpoints.get(id);
  assert.ok(checkpoint, `UI sidecar has no ${id} checkpoint`);
  for (const assertion of checkpoint.assertions) {
    await page
      .getByRole(assertion.role, {
        name: new RegExp(escapeRegExp(assertion.name), "i"),
      })
      .first()
      .waitFor();
  }
  if (checkpoint.screenshot) {
    await page.screenshot({
      path: path.join(outputDirectory, `${id}.png`),
      fullPage: true,
    });
  }
}

async function drive(command) {
  const response = await fetch(`${baseURL}/v1/lab/drive`, {
    method: "POST",
    headers: labHeaders({ "Content-Type": "application/json" }),
    body: JSON.stringify(command),
  });
  if (!response.ok) {
    throw new Error(`Lab drive returned ${response.status}: ${await response.text()}`);
  }
  return response.json();
}

async function restart() {
  const response = await fetch(`${baseURL}/v1/lab/restart`, {
    method: "POST",
    headers: labHeaders(),
  });
  if (!response.ok) {
    throw new Error(`Lab restart returned ${response.status}: ${await response.text()}`);
  }
}

async function normalJSON(route) {
  const response = await context.request.get(`${baseURL}${route}`);
  if (!response.ok()) {
    throw new Error(`${route} returned ${response.status()}: ${await response.text()}`);
  }
  return response.json();
}

async function saveBundle() {
  const response = await fetch(`${baseURL}/v1/lab/bundle`, {
    headers: labHeaders(),
  });
  if (!response.ok) {
    const body = await response.text();
    throw new Error(`Lab bundle returned ${response.status}: ${body}`);
  }
  await fs.writeFile(
    path.join(outputDirectory, "artifact-warmth-restart.mlab"),
    Buffer.from(await response.arrayBuffer()),
  );
}

async function uploadEvidence() {
  const form = new FormData();
  const trace = await fs.readFile(path.join(outputDirectory, "trace.zip"));
  form.append("trace", new Blob([trace]), "trace.zip");
  for (const name of (await fs.readdir(outputDirectory)).sort()) {
    if (!name.endsWith(".png")) continue;
    const screenshot = await fs.readFile(path.join(outputDirectory, name));
    form.append("screenshots", new Blob([screenshot]), name);
  }
  const response = await fetch(`${baseURL}/v1/lab/evidence`, {
    method: "POST",
    headers: labHeaders(),
    body: form,
  });
  if (!response.ok) {
    throw new Error(
      `Lab evidence upload returned ${response.status}: ${await response.text()}`,
    );
  }
}

function labHeaders(extra = {}) {
  return { Authorization: `Bearer ${token}`, ...extra };
}

async function retainFailure(action) {
  try {
    await action();
  } catch (error) {
    failure = failure
      ? new AggregateError([failure, error], "Lab browser proof failed")
      : error;
  }
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
