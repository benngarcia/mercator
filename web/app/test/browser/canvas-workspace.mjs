import assert from "node:assert/strict";

import { chromium } from "playwright";

const baseURL = (
  process.env.MERCATOR_BROWSER_BASE_URL ?? "http://127.0.0.1:3000"
).replace(/\/$/, "");
const workspaceID = "ws_scenario";
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
  page.setDefaultTimeout(10_000);
  await page.route("**/v1/workspaces*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: '{"workspaces":[]}',
    }),
  );
  const consoleProblems = [];
  page.on("console", (message) => {
    if (message.type() === "error" || message.type() === "warning") {
      consoleProblems.push(`${message.type()}: ${message.text()}`);
    }
  });
  page.on("pageerror", (error) => consoleProblems.push(error.message));
  const url = new URL("/canvas", baseURL);
  url.searchParams.set("workspace_id", workspaceID);
  url.searchParams.set("scenario", "full-schedule-forces-fresh-capacity");
  url.searchParams.set("play", "1");
  await page.goto(url.toString(), { waitUntil: "domcontentloaded" });

  await page.getByText("rental-warm", { exact: true }).waitFor();
  const eventFeed = page.getByRole("region", { name: "Workspace events" });
  await eventFeed.waitFor();
  assert.equal(await eventFeed.locator('[data-event-id="scenario-10"]').count(), 1);
  const progress = page.getByRole("progressbar", { name: "Scenario progress" });
  await page.getByRole("button", { name: "Pause scenario" }).click();
  await page.getByRole("button", { name: "Play scenario" }).waitFor();
  const pausedAt = Number(await progress.getAttribute("aria-valuenow"));
  await page.waitForTimeout(600);
  assert.equal(Number(await progress.getAttribute("aria-valuenow")), pausedAt);
  await page.getByText("Event 0 of 14", { exact: true }).waitFor();
  assert.equal(
    await page
      .getByText("The warm Rental would win on score", { exact: false })
      .count(),
    0,
  );
  for (const label of ["Requested", "Provisioning", "Running"]) {
    assert.equal(await page.getByRole("tab", { name: label }).count(), 0);
  }
  assert.equal(
    await page
      .locator('[aria-label="4 of 4 Booking positions occupied"]')
      .count(),
    1,
  );
  await page.getByText("$2.50/h", { exact: true }).waitFor();
  const queuedRun = page.locator('a[aria-label^="run-q1:"]');
  const queuedRunPosition = await queuedRun.boundingBox();
  assert.ok(queuedRunPosition, "queued Run needs a rendered card");
  assert.ok(
    queuedRunPosition.width >= 72,
    `queued Run was compressed to ${queuedRunPosition.width}px`,
  );

  const movingRun = page.locator('a[aria-label^="run-fifth:"]');
  await page.getByRole("button", { name: "Next event" }).click();
  await movingRun.waitFor();
  await eventFeed.locator('[data-event-id="scenario-11"]').waitFor();
  await page.getByText("Event 1 of 14", { exact: true }).waitFor();
  const incomingPosition = await movingRun.boundingBox();
  assert.ok(incomingPosition, "incoming Run needs a rendered card");
  await page.getByRole("button", { name: "Next event" }).click();
  await eventFeed.locator('[data-event-id="scenario-12"]').waitFor();
  await page.getByText("Event 2 of 14", { exact: true }).waitFor();
  await page.waitForFunction(
    ({ x, y }) => {
      const card = document.querySelector('a[aria-label^="run-fifth:"]');
      if (!(card instanceof HTMLElement)) return false;
      const position = card.getBoundingClientRect();
      return position.y > y + 80 && position.x > x + 300;
    },
    { x: incomingPosition.x, y: incomingPosition.y },
  );
  const rentalPosition = await movingRun.boundingBox();
  assert.equal(
    await eventFeed.locator("[data-event-id]").first().getAttribute("data-event-id"),
    "scenario-12",
  );
  assert.ok(rentalPosition, "booked Run needs a rendered card");
  assert.ok(
    rentalPosition.y > incomingPosition.y + 80,
    `Run card did not move from intake to its Rental: ${incomingPosition.y} -> ${rentalPosition.y}`,
  );
  assert.ok(
    rentalPosition.x > incomingPosition.x + 300,
    `Run card did not move to its projected time: ${incomingPosition.x} -> ${rentalPosition.x}`,
  );
  await page.getByRole("button", { name: "Previous event" }).click();
  await page.getByText("Event 1 of 14", { exact: true }).waitFor();
  await eventFeed.locator('[data-event-id="scenario-12"]').waitFor({
    state: "detached",
  });
  assert.equal(
    await eventFeed
      .locator("[data-event-id]")
      .first()
      .getAttribute("data-event-id"),
    "scenario-11",
  );
  await page.getByRole("button", { name: "Restart scenario" }).click();
  await page.getByRole("button", { name: "Pause scenario" }).click();
  await page.getByText("Event 0 of 14", { exact: true }).waitFor();
  await page.getByRole("button", { name: "4× playback speed" }).click();
  await page
    .getByRole("button", { name: "4× playback speed", pressed: true })
    .waitFor();
  assert.deepEqual(consoleProblems, []);

  if (process.env.MERCATOR_BROWSER_SCREENSHOT) {
    await page.screenshot({
      path: process.env.MERCATOR_BROWSER_SCREENSHOT,
      fullPage: false,
    });
  }
  await queuedRun.focus();
  await queuedRun.press("Enter");
  await page.waitForURL(/\/runs\/run-q1/);
  assert.equal(new URL(page.url()).pathname, "/runs/run-q1");
} finally {
  await context.close();
  await browser.close();
}
