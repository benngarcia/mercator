import assert from "node:assert/strict";

import { chromium } from "playwright";

const baseURL = (process.env.MERCATOR_BROWSER_BASE_URL ?? "http://127.0.0.1:3000").replace(
  /\/$/,
  "",
);
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
  url.searchParams.set(
    "scenario",
    "full-schedule-forces-fresh-capacity",
  );
  url.searchParams.set("play", "1");
  await page.goto(url.toString(), { waitUntil: "domcontentloaded" });

  await page.getByText("rental-warm", { exact: true }).waitFor();
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

  const movingRun = page.locator('a[aria-label^="run-fifth:"]');
  await movingRun.waitFor();
  const incomingPosition = await movingRun.boundingBox();
  assert.ok(incomingPosition, "incoming Run needs a rendered card");
  await page.waitForTimeout(1_100);
  const rentalPosition = await movingRun.boundingBox();
  assert.ok(rentalPosition, "booked Run needs a rendered card");
  assert.ok(
    rentalPosition.y > incomingPosition.y + 80,
    `Run card did not move from intake to its Rental: ${incomingPosition.y} -> ${rentalPosition.y}`,
  );
  assert.ok(
    rentalPosition.x > incomingPosition.x + 300,
    `Run card did not move to its projected time: ${incomingPosition.x} -> ${rentalPosition.x}`,
  );
  assert.deepEqual(consoleProblems, []);

  if (process.env.MERCATOR_BROWSER_SCREENSHOT) {
    await page.screenshot({
      path: process.env.MERCATOR_BROWSER_SCREENSHOT,
      fullPage: false,
    });
  }
  await movingRun.click();
  await page.waitForURL(/\/runs\/run-fifth/);
  assert.equal(new URL(page.url()).pathname, "/runs/run-fifth");
} finally {
  await context.close();
  await browser.close();
}
