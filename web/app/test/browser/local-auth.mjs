import assert from "node:assert/strict";

import { chromium } from "playwright";

const baseURL = (
  process.env.MERCATOR_BROWSER_BASE_URL ?? "http://localhost:3000"
).replace(/\/$/, "");
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({
  viewport: { width: 1440, height: 960 },
  colorScheme: "dark",
});
await context.addInitScript(() => {
  localStorage.setItem("mercator.token", "stale-token");
  localStorage.removeItem("mercator.workspace");
});

const page = await context.newPage();
page.setDefaultTimeout(10_000);
const consoleProblems = [];
page.on("console", (message) => {
  if (message.type() === "error" || message.type() === "warning") {
    consoleProblems.push(`${message.type()}: ${message.text()}`);
  }
});
page.on("pageerror", (error) => consoleProblems.push(error.message));

try {
  // Arrange
  const url = new URL("/canvas", baseURL);
  url.searchParams.set("workspace_id", "ws_scenario");
  url.searchParams.set("scenario", "warm-pool-burst");
  await page.goto(url.toString(), { waitUntil: "domcontentloaded" });
  await page.getByText("developer@localhost", { exact: true }).waitFor();
  await page.getByText("rental-warm", { exact: true }).waitFor();
  const firstCookie = (await context.cookies()).find(
    (cookie) => cookie.name === "mercator_session",
  );

  // Act
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.getByText("developer@localhost", { exact: true }).waitFor();
  await page.getByText("rental-warm", { exact: true }).waitFor();
  const reloadedCookie = (await context.cookies()).find(
    (cookie) => cookie.name === "mercator_session",
  );

  // Assert
  assert.ok(firstCookie, "local auth should establish a session cookie");
  assert.equal(firstCookie.httpOnly, true);
  assert.equal(firstCookie.sameSite, "Lax");
  assert.equal(reloadedCookie?.value, firstCookie.value);
  assert.equal(await page.getByLabel("Set API token").count(), 0);
  assert.equal(
    await page.evaluate(() => localStorage.getItem("mercator.token")),
    null,
  );
  assert.equal(
    await page.evaluate(() => localStorage.getItem("mercator.workspace")),
    "ws_scenario",
  );
  assert.deepEqual(consoleProblems, []);

  if (process.env.MERCATOR_BROWSER_SCREENSHOT) {
    await page.screenshot({
      path: process.env.MERCATOR_BROWSER_SCREENSHOT,
      fullPage: false,
    });
  }
} catch (error) {
  console.error(
    JSON.stringify({
      url: page.url(),
      text: (await page.locator("body").innerText()).slice(0, 2_000),
      consoleProblems,
    }),
  );
  throw error;
} finally {
  await context.close();
  await browser.close();
}
