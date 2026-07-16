// Records the launch-facing Mercator console demo against a running broker.
// The flow uses real console interactions and writes an ignored WebM under
// docs/assets/output for review before a selected recording is committed.

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { chromium } from "playwright";

function requiredEnv(name) {
  const value = process.env[name]?.trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");
const baseURL = requiredEnv("MERCATOR_API_URL").replace(/\/$/, "");
const token = requiredEnv("MERCATOR_API_TOKEN");
const workspaceID = requiredEnv("MERCATOR_WORKSPACE_ID");
const outputDirectory = process.env.OUT
  ? path.resolve(process.env.OUT)
  : path.join(repoRoot, "docs/assets/output");
const headers = {
  Authorization: `Bearer ${token}`,
  "Content-Type": "application/json",
};

function localDockerArchitecture() {
  const architecture = execFileSync(
    "docker",
    ["info", "--format", "{{.Architecture}}"],
    { encoding: "utf8" },
  ).trim();
  if (architecture === "x86_64" || architecture === "amd64") return "amd64";
  if (architecture === "aarch64" || architecture === "arm64") return "arm64";
  throw new Error(`unsupported Docker architecture: ${architecture}`);
}

async function seedRunIfEmpty() {
  const response = await fetch(
    `${baseURL}/v1/runs?workspace_id=${encodeURIComponent(workspaceID)}`,
    { headers },
  );
  if (!response.ok) {
    throw new Error(`list runs returned ${response.status}: ${await response.text()}`);
  }
  if ((await response.json()).runs?.some((run) => run.outcome === "succeeded")) {
    return;
  }

  execFileSync("docker", ["pull", "-q", "busybox:latest"], { stdio: "ignore" });
  const architecture = localDockerArchitecture();
  const image = execFileSync(
    "docker",
    ["inspect", "--format", "{{index .RepoDigests 0}}", "busybox:latest"],
    { encoding: "utf8" },
  ).trim();
  const create = await fetch(
    `${baseURL}/v1/runs?workspace_id=${encodeURIComponent(workspaceID)}`,
    {
      method: "POST",
      headers: {
        ...headers,
        "Idempotency-Key": `console-demo-seed-${architecture}`,
      },
      body: JSON.stringify({
        workload: {
          workspace_id: workspaceID,
          spec: {
            containers: [
              {
                name: "main",
                image,
                platform: { os: "linux", architecture },
                args: ["echo", "hi"],
              },
            ],
          },
        },
      }),
    },
  );
  if (!create.ok) {
    throw new Error(`seed run returned ${create.status}: ${await create.text()}`);
  }
}

await seedRunIfEmpty();
fs.rmSync(outputDirectory, { recursive: true, force: true });
fs.mkdirSync(outputDirectory, { recursive: true });

const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({
  viewport: { width: 1280, height: 800 },
  deviceScaleFactor: 1,
  colorScheme: "dark",
  recordVideo: {
    dir: outputDirectory,
    size: { width: 1280, height: 800 },
  },
});

try {
  await context.addInitScript(
    ([sessionToken, sessionWorkspace]) => {
      localStorage.setItem("mercator.token", sessionToken);
      localStorage.setItem("mercator.workspace", sessionWorkspace);
      localStorage.setItem(
        "mercator.recentWorkspaces",
        JSON.stringify([sessionWorkspace]),
      );
    },
    [token, workspaceID],
  );

  const page = await context.newPage();
  await page.goto(
    `${baseURL}/runs?workspace_id=${encodeURIComponent(workspaceID)}`,
    { waitUntil: "domcontentloaded" },
  );
  await page.getByText("succeeded", { exact: false }).first().waitFor();
  await page.waitForTimeout(1_000);

  await page.locator("tbody tr").first().click();
  await page.getByText("Selected offer", { exact: false }).first().waitFor();
  await page.waitForTimeout(2_000);

  await page.getByRole("tab", { name: "Events" }).click();
  await page.getByText("Closed", { exact: false }).first().waitFor();
  await page.waitForTimeout(2_500);
} finally {
  await context.close();
  await browser.close();
}

const recordings = fs
  .readdirSync(outputDirectory)
  .filter((file) => file.endsWith(".webm"));
if (recordings.length !== 1) {
  throw new Error(`expected one recording, found ${recordings.length}`);
}
const recording = path.join(outputDirectory, recordings[0]);
const destination = path.join(outputDirectory, "mercator-demo.webm");
fs.renameSync(recording, destination);
console.log(`wrote ${destination}`);
