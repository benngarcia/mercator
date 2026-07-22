import assert from "node:assert/strict";

import { chromium } from "playwright";

const baseURL = (
  process.env.MERCATOR_BROWSER_BASE_URL ?? "http://127.0.0.1:3000"
).replace(/\/$/, "");
const workspaceID = `ws_browser_${process.pid}`;
const requestedEventID = `evt_${workspaceID}_run-provider-replacement_requested`;
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
  let feedResponses = 0;
  page.on("response", (response) => {
    if (response.url().includes("/v1/console/events")) feedResponses += 1;
    if (
      process.env.DEBUG_BROWSER &&
      (response.url().includes("/v1/console/events") ||
        response.url().includes("/v1/dev/scenario-sessions/"))
    ) {
      console.log(`Response ${response.status()} ${response.url()}`);
    }
  });
  if (process.env.DEBUG_BROWSER) {
    console.log(`Workspace ${workspaceID}`);
    page.on("requestfailed", (request) => {
      if (request.url().includes("/v1/")) {
        console.log(`Request failed ${request.failure()?.errorText} ${request.url()}`);
      }
    });
  }
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
  const progress = page.getByRole("progressbar", { name: "Scenario progress" });
  const pauseScenario = page.getByRole("button", { name: "Pause scenario" });
  if (await pauseScenario.isVisible()) {
    await playbackCommand(page, "Pause scenario");
    await page.getByRole("button", { name: "Play scenario" }).waitFor();
  }
  await playbackCommand(page, "Restart scenario");
  await pauseScenario.waitFor();
  await page.getByText("Event 0 of 46", { exact: true }).waitFor();
  await playbackCommand(page, "Pause scenario");
  await page.getByRole("button", { name: "Play scenario" }).waitFor();
  const pausedAt = Number(await progress.getAttribute("aria-valuenow"));
  await page.waitForTimeout(600);
  assert.equal(Number(await progress.getAttribute("aria-valuenow")), pausedAt);
  const stableFeedResponses = feedResponses;
  await page.getByText("Event 0 of 46", { exact: true }).waitFor();
  await eventFeed
    .getByText("Sanitized recorded offers · 4 production paths", {
      exact: true,
    })
    .waitFor();
  await eventFeed.getByText("Target contract: rental_schedule", { exact: true }).waitFor();
  for (const offerID of [
    "off_vast_9001",
    "off_runpod_secure_rtx_4090",
    "off_shadeform_hyperstack_a6000",
  ]) {
    assert.equal(await page.locator(`[aria-label^="${offerID}:"]`).count(), 1);
  }
  assert.equal(
    await page
      .locator('[aria-label="1 of 4 Booking positions occupied"]')
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

  const movingRun = page.locator(
    'a[aria-label^="run-provider-replacement:"]',
  );
  await playbackCommand(page, "Next event");
  await movingRun.waitFor();
  await eventFeed
    .locator(`[data-event-id="${requestedEventID}"]`)
    .waitFor();
  await page.getByText("Event 1 of 46", { exact: true }).waitFor();
  await waitForViewTransitionAnimation(page);
  const incomingPosition = await movingRun.boundingBox();
  assert.ok(incomingPosition, "incoming Run needs a rendered card");
  await page.waitForTimeout(450);
  await playbackCommand(page, "Next event");
  await page.getByText("Event 2 of 46", { exact: true }).waitFor();
  await eventFeed.getByText("Booking decided", { exact: true }).first().waitFor();
  await waitForViewTransitionAnimation(page);
  await page.waitForFunction(
    ({ y }) => {
      const card = document.querySelector(
        'a[aria-label^="run-provider-replacement:"]',
      );
      if (!(card instanceof HTMLElement)) return false;
      const position = card.getBoundingClientRect();
      return position.y > y + 80;
    },
    { y: incomingPosition.y },
  );
  const rentalPosition = await movingRun.boundingBox();
  assert.ok(rentalPosition, "booked Run needs a rendered card");
  assert.ok(
    rentalPosition.y > incomingPosition.y + 80,
    `Run card did not move from intake to its Rental: ${incomingPosition.y} -> ${rentalPosition.y}`,
  );
  await page.waitForTimeout(450);
  await playbackCommand(page, "Previous event");
  await page.getByText("Event 1 of 46", { exact: true }).waitFor();
  assert.equal(
    await eventFeed
      .locator("[data-event-id]")
      .first()
      .getAttribute("data-event-id"),
    requestedEventID,
  );
  await stepTo(page, progress, 5);
  await eventFeed.getByText("Launch failed", { exact: true }).first().waitFor();
  await stepTo(page, progress, 6);
  assert.equal(
    await eventFeed.getByText("Booking decided", { exact: true }).count(),
    4,
  );
  await stepTo(page, progress, 38);
  await eventFeed.locator('[data-event-id="target-q1-queued"]').waitFor();
  await stepTo(page, progress, 42);
  await eventFeed.locator('[data-event-id="target-q1-dispatched"]').waitFor();
  await stepTo(page, progress, 46);
  await eventFeed.locator('[data-event-id="target-q1-closed"]').waitFor();

  await playbackCommand(page, "Restart scenario");
  await pauseScenario.waitFor();
  await page.getByText("Event 0 of 46", { exact: true }).waitFor();
  await playbackCommand(page, "Pause scenario");
  await page.getByText("Event 0 of 46", { exact: true }).waitFor();
  await playbackCommand(page, "4× playback speed");
  await page
    .getByRole("button", { name: "4× playback speed", pressed: true })
    .waitFor();
  assert.equal(
    feedResponses,
    stableFeedResponses,
    "scenario controls must not reconnect the Workspace event feed",
  );
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

async function stepTo(page, progress, target) {
  let current = Number(await progress.getAttribute("aria-valuenow"));
  while (current < target) {
    if (process.env.DEBUG_BROWSER) console.log(`Stepping to event ${current + 1}`);
    await playbackCommand(page, "Next event");
    await page.waitForFunction(
      (expected) =>
        document
          .querySelector('[role="progressbar"][aria-label="Scenario progress"]')
          ?.getAttribute("aria-valuenow") === String(expected),
      current + 1,
    );
    await page.waitForTimeout(450);
    current += 1;
  }
}

async function playbackCommand(page, accessibleName) {
  await page.getByRole("button", { name: accessibleName }).click();
}

async function waitForViewTransitionAnimation(page) {
  await page.waitForFunction(() =>
    document.getAnimations().some((animation) => {
      const pseudoElement = animation.effect?.pseudoElement;
      return (
        animation.playState === "running" &&
        pseudoElement?.startsWith("::view-transition")
      );
    }),
  );
}
