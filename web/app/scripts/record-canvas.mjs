import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { chromium } from "playwright";

const baseURL = (process.env.MERCATOR_BROWSER_BASE_URL ?? "http://127.0.0.1:3000").replace(
  /\/$/,
  "",
);
const repoRoot = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../../..",
);
const outputDirectory = process.env.OUT
  ? path.resolve(process.env.OUT)
  : path.join(repoRoot, "output/canvas");
const workspaceID = "ws_scenario";
fs.mkdirSync(outputDirectory, { recursive: true });

const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({
  viewport: { width: 1440, height: 960 },
  colorScheme: "dark",
  recordVideo: {
    dir: outputDirectory,
    size: { width: 1440, height: 960 },
  },
});

try {
  await context.addInitScript((workspace) => {
    localStorage.setItem("mercator.workspace", workspace);
  }, workspaceID);
  const page = await context.newPage();
  await page.route("**/v1/workspaces*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: '{"workspaces":[]}',
    }),
  );
  const url = new URL("/canvas", baseURL);
  url.searchParams.set("workspace_id", workspaceID);
  url.searchParams.set(
    "scenario",
    "full-schedule-forces-fresh-capacity",
  );
  url.searchParams.set("play", "1");
  await page.goto(url.toString(), { waitUntil: "domcontentloaded" });
  await page.getByText("rental-warm", { exact: true }).waitFor();
  await page.waitForTimeout(500);
  const movingRun = page.locator('a[aria-label^="run-fifth:"]');
  await movingRun.waitFor();
  await page.waitForTimeout(2_000);
} finally {
  await context.close();
  await browser.close();
}

const recordings = fs
  .readdirSync(outputDirectory)
  .filter((file) => file.endsWith(".webm"))
  .map((file) => path.join(outputDirectory, file))
  .sort((a, b) => fs.statSync(b).mtimeMs - fs.statSync(a).mtimeMs);
if (!recordings[0]) throw new Error("Canvas recording was not created");
const destination = path.join(outputDirectory, "mercator-canvas.webm");
if (recordings[0] !== destination) fs.renameSync(recordings[0], destination);
console.log(`wrote ${destination}`);
