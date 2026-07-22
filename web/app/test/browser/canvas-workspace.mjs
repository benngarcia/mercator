import assert from "node:assert/strict";

import { chromium } from "playwright";

const baseURL = (
  process.env.MERCATOR_BROWSER_BASE_URL ?? "http://127.0.0.1:3000"
).replace(/\/$/, "");
const workspaceID = `ws_browser_${process.pid}`;
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({
  viewport: { width: 1440, height: 960 },
  colorScheme: "dark",
});

try {
  await context.addInitScript((workspace) => {
    localStorage.setItem("mercator.workspace", workspace);
  }, workspaceID);
  const page = await context.newPage();
  page.setDefaultTimeout(15_000);
  const consoleProblems = [];
  page.on("console", (message) => {
    if (message.type() === "error" || message.type() === "warning") {
      consoleProblems.push(`${message.type()}: ${message.text()}`);
    }
  });
  page.on("pageerror", (error) => consoleProblems.push(error.message));
  await page.route("**/v1/workspaces*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: '{"workspaces":[]}',
    }),
  );

  const url = new URL("/canvas", baseURL);
  url.searchParams.set("workspace_id", workspaceID);
  url.searchParams.set("scenario", "warm-pool-burst");
  url.searchParams.set("play", "1");
  await page.goto(url.toString(), { waitUntil: "domcontentloaded" });

  await page.getByLabel("Workspace events live").waitFor();
  await page.getByText("rental-warm", { exact: true }).waitFor();
  const eventFeed = page.getByRole("region", { name: "Workspace events" });
  const progress = page.getByRole("progressbar", { name: "Scenario progress" });
  await page.getByLabel("Placement scenario").waitFor();
  assert.equal(await page.getByLabel("Placement scenario").inputValue(), "warm-pool-burst");
  await eventFeed
    .getByText("Sanitized recorded offers · 6 production paths", { exact: true })
    .waitFor();
  assert.equal(await eventFeed.getByText(/Target contract:/).count(), 0);

  if (await page.getByRole("button", { name: "Pause scenario" }).isVisible()) {
    await playbackCommand(page, "Pause scenario");
  }
  await playbackCommand(page, "Restart scenario");
  await playbackCommand(page, "Pause scenario");
  await waitForCursor(page, 0);
  const cueCount = Number(await progress.getAttribute("aria-valuemax"));
  assert.ok(cueCount > 40, `warm-pool scenario only has ${cueCount} events`);

  await playbackCommand(page, "Next event");
  await waitForCursor(page, 1);
  const firstRun = page.locator('a[aria-label^="run-burst-01:"]');
  await firstRun.waitFor();
  const incoming = await firstRun.boundingBox();
  assert.ok(incoming, "first Run needs an intake card");

  await playbackCommand(page, "Next event");
  await waitForCursor(page, 2);
  await eventFeed.getByText("reuse now", { exact: true }).first().waitFor();
  await eventFeed.getByText("9 candidates", { exact: true }).first().waitFor();
  const placed = await firstRun.boundingBox();
  assert.ok(placed && placed.y > incoming.y + 70, "first Run did not move onto its Rental");

  await stepTo(page, progress, 8);
  await eventFeed.getByText("queue", { exact: true }).first().waitFor();
  const queuedRun = page.locator('a[aria-label^="run-burst-02:"]');
  const queuedPosition = await queuedRun.boundingBox();
  assert.ok(queuedPosition && queuedPosition.width >= 72, "queued Run was compressed");

  const queuedDecision = eventFeed
    .locator("li")
    .filter({ hasText: "Booking decided" })
    .filter({ hasText: "queue" })
    .first();
  await queuedDecision.getByRole("button").click();
  await queuedDecision.getByText("off_vast_9002", { exact: true }).waitFor();
  await queuedDecision.getByText("fresh", { exact: true }).first().waitFor();

  await stepTo(page, progress, cueCount);
  await page.getByText(`Event ${cueCount} of ${cueCount}`, { exact: true }).waitFor();

  await page.getByLabel("Placement scenario").selectOption("deadline-versus-cost");
  await page.waitForURL(/scenario=deadline-versus-cost/);
  await waitForScenario(page, "deadline-versus-cost");
  await page.getByLabel("Workspace events live").waitFor();
  await eventFeed.getByText("runs/run-deadline-urgent", { exact: true }).first().waitFor();
  const urgentDecision = eventFeed
    .locator("li")
    .filter({ hasText: "Booking decided" })
    .filter({ hasText: "runs/run-deadline-urgent" });
  await revealEvidence(urgentDecision, "LATENCY_SLO_EXCEEDED");

  await page.getByLabel("Placement scenario").selectOption("failure-rebalance");
  await page.waitForURL(/scenario=failure-rebalance/);
  await waitForScenario(page, "failure-rebalance");
  await page.getByLabel("Workspace events live").waitFor();
  await eventFeed.getByText("runs/run-provider-replacement", { exact: true }).first().waitFor();
  await eventFeed.getByText("Launch failed", { exact: true }).waitFor();
  const replacementDecisions = eventFeed
    .locator("li")
    .filter({ hasText: "Booking decided" })
    .filter({ hasText: "runs/run-provider-replacement" });
  await revealEvidence(
    replacementDecisions.first(),
    "PREVIOUS_ATTEMPT_CAPACITY_UNAVAILABLE",
  );

  assert.deepEqual(consoleProblems, []);
  if (process.env.MERCATOR_BROWSER_SCREENSHOT) {
    await page.screenshot({
      path: process.env.MERCATOR_BROWSER_SCREENSHOT,
      fullPage: false,
    });
  }
} finally {
  await context.close();
  await browser.close();
}

async function stepTo(page, progress, target) {
  let current = Number(await progress.getAttribute("aria-valuenow"));
  while (current < target) {
    await playbackCommand(page, "Next event");
    current += 1;
    await waitForCursor(page, current);
    await page.waitForTimeout(80);
  }
}

async function waitForCursor(page, expected) {
  await page.waitForFunction(
    (cursor) =>
      document
        .querySelector('[role="progressbar"][aria-label="Scenario progress"]')
        ?.getAttribute("aria-valuenow") === String(cursor),
    expected,
  );
}

async function playbackCommand(page, accessibleName) {
  await page.getByRole("button", { name: accessibleName }).click();
}

async function waitForScenario(page, scenario) {
  await page.waitForFunction(
    (expected) =>
      document.querySelector('[aria-label="Placement scenario"]')?.value === expected,
    scenario,
  );
}

async function revealEvidence(row, evidence) {
  for (let attempt = 0; attempt < 4; attempt += 1) {
    if ((await row.innerText()).includes(evidence)) return;
    const button = row.getByRole("button");
    if ((await button.getAttribute("aria-expanded")) !== "true") {
      await button.click();
    }
    await row.page().waitForTimeout(250);
  }
  assert.fail(`Decision row did not reveal ${evidence}: ${await row.innerText()}`);
}
