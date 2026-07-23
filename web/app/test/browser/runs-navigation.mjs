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
const fixturePath = path.resolve(requiredEnv("MERCATOR_BROWSER_FIXTURE"));
const outputDirectory = path.resolve(requiredEnv("MERCATOR_BROWSER_OUTPUT"));
const fixture = JSON.parse(await fs.readFile(fixturePath, "utf8"));
const workspaceID = requiredEnv("MERCATOR_BROWSER_WORKSPACE_ID");
await fs.mkdir(outputDirectory, { recursive: true });

const browser = await chromium.launch({ headless: true });
const browserFailures = [];

function expectedMissingDecision(response) {
  const url = new URL(response.url());
  return (
    response.status() === 404 &&
    response.request().method() === "GET" &&
    /^\/v1\/runs\/[^/]+\/decision$/.test(url.pathname)
  );
}

function recordBrowserFailures(page) {
  page.on("console", (message) => {
    if (
      message.type() === "error" &&
      !message.text().startsWith("Failed to load resource:")
    ) {
      browserFailures.push(message.text());
      console.error(`browser console: ${message.text()}`);
    }
  });
  page.on("pageerror", (error) => {
    browserFailures.push(error.message);
    console.error(`browser page error: ${error.message}`);
  });
  page.on("response", (response) => {
    if (response.status() >= 400 && !expectedMissingDecision(response)) {
      const failure = `${response.request().method()} ${response.url()}: ${response.status()}`;
      browserFailures.push(failure);
      console.error(`browser HTTP failure: ${failure}`);
    }
  });
}

function contextOptions(viewport) {
  return {
    viewport,
    deviceScaleFactor: 1,
    colorScheme: "dark",
  };
}

async function prepareContext(viewport) {
  const context = await browser.newContext(contextOptions(viewport));
  await context.addInitScript(
    (workspace) => {
      localStorage.setItem("mercator.workspace", workspace);
      localStorage.setItem(
        "mercator.recentWorkspaces",
        JSON.stringify([workspace]),
      );
    },
    workspaceID,
  );
  return context;
}

function runsURL(pathname = "/runs") {
  const url = new URL(pathname, baseURL);
  url.searchParams.set("workspace_id", workspaceID);
  return url.toString();
}

async function waitForRuns(page) {
  await page.getByRole("heading", { name: "Runs", exact: true }).waitFor();
  await page
    .getByRole("button", { name: "Create run", exact: true })
    .first()
    .waitFor();
}

async function localSessionSurvivesReload(page) {
  // Arrange
  await page.goto(runsURL(), { waitUntil: "domcontentloaded" });
  await waitForRuns(page);

  // Act
  await page.reload({ waitUntil: "domcontentloaded" });
  await waitForRuns(page);

  // Assert
  await page.getByText("developer@localhost", { exact: true }).waitFor();
  assert.equal(await page.getByLabel("Set API token").count(), 0);
  assert.equal(
    await page.evaluate(() => localStorage.getItem("mercator.token")),
    null,
  );
}

async function createdRunRequest(page, submit) {
  const requestPromise = page.waitForRequest(
    (request) =>
      request.method() === "POST" &&
      new URL(request.url()).pathname === "/v1/runs",
  );
  const responsePromise = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname === "/v1/runs",
  );
  await submit();
  return Promise.all([requestPromise, responsePromise]);
}

async function runsScopeCancelledCreate(page) {
  // Arrange
  await page.goto(runsURL(), { waitUntil: "domcontentloaded" });
  await waitForRuns(page);
  await page
    .getByRole("button", { name: "Copy run id", exact: true })
    .first()
    .waitFor();
  await page.getByRole("button", { name: "Run", exact: true }).click();
  const sortedColumn = page.locator('th[aria-sort="ascending"]');
  assert.equal(
    await sortedColumn
      .getByRole("button", { name: "Run", exact: true })
      .count(),
    1,
  );
  const originalURL = page.url();
  const trigger = page
    .getByRole("button", { name: "Create run", exact: true })
    .first();
  let mutations = 0;
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname === "/v1/runs"
    ) {
      mutations += 1;
    }
  });

  // Act
  await trigger.focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog", { name: "Create run" });
  await dialog.waitFor();

  // Assert
  assert.equal(new URL(page.url()).searchParams.get("action"), "create");
  await dialog
    .getByText("Place a workload on an offer", { exact: false })
    .waitFor();
  assert.equal(
    await dialog.evaluate((element) =>
      element.contains(document.activeElement),
    ),
    true,
  );
  await dialog.getByRole("tab", { name: "Spec" }).click();
  await dialog.getByLabel("Workload revision JSON").waitFor();
  await page.keyboard.press("Escape");
  await page.waitForURL(originalURL);
  assert.equal(mutations, 0);
  assert.equal(
    await sortedColumn
      .getByRole("button", { name: "Run", exact: true })
      .count(),
    1,
  );
  assert.equal(
    await trigger.evaluate((element) => element === document.activeElement),
    true,
  );
}

async function minimalImageRunCreated(page) {
  // Arrange
  const originatingURL = page.url();
  await page
    .getByRole("button", { name: "Create run", exact: true })
    .first()
    .click();
  const dialog = page.getByRole("dialog", { name: "Create run" });
  await dialog.getByLabel("Image").fill(fixture.image.reference);
  for (const argument of fixture.image.args) {
    await dialog.getByRole("button", { name: "Add argument" }).click();
    await dialog
      .getByLabel(/^Argument /)
      .last()
      .fill(argument);
  }
  for (const [name, value] of Object.entries(fixture.image.env)) {
    await dialog.getByRole("button", { name: "Add variable" }).click();
    await dialog.getByLabel("Environment variable name").last().fill(name);
    await dialog.getByLabel("Environment variable value").last().fill(value);
  }

  // Act
  const [request, response] = await createdRunRequest(page, () =>
    dialog.getByRole("button", { name: "Create run", exact: true }).click(),
  );
  const responseBody = await response.json();

  // Assert
  assert.equal(response.status(), 202);
  assert.ok(request.headers()["idempotency-key"]);
  assert.equal(
    new URL(request.url()).searchParams.get("workspace_id"),
    workspaceID,
  );
  assert.deepEqual(request.postDataJSON(), {
    image: fixture.image.reference,
    args: fixture.image.args,
    env: Object.fromEntries(
      Object.entries(fixture.image.env).map(([name, value]) => [
        name,
        { value },
      ]),
    ),
  });
  await page.waitForURL(
    (url) => url.pathname === `/runs/${responseBody.run_id}`,
  );
  assert.equal(
    new URL(page.url()).searchParams.get("workspace_id"),
    workspaceID,
  );
  await page.goBack();
  await page.waitForURL(originatingURL);
  await waitForRuns(page);
  assert.equal(
    await page.getByRole("dialog", { name: "Create run" }).count(),
    0,
  );
}

async function immutableSpecRunCreated(page) {
  // Arrange
  await page
    .getByRole("button", { name: "Create run", exact: true })
    .first()
    .click();
  const dialog = page.getByRole("dialog", { name: "Create run" });
  await dialog.getByRole("tab", { name: "Spec" }).click();
  const editor = dialog.getByLabel("Workload revision JSON");
  let invalidMutations = 0;
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname === "/v1/runs"
    ) {
      invalidMutations += 1;
    }
  });
  await editor.fill("{");
  await dialog.getByRole("button", { name: "Create run", exact: true }).click();
  await page.getByText("Workload JSON is not valid", { exact: true }).waitFor();
  assert.equal(new URL(page.url()).searchParams.get("action"), "create");
  assert.equal(invalidMutations, 0);
  await editor.fill(JSON.stringify(fixture.workload, null, 2));

  // Act
  const [request, response] = await createdRunRequest(page, () =>
    dialog.getByRole("button", { name: "Create run", exact: true }).click(),
  );
  const responseBody = await response.json();

  // Assert
  assert.equal(response.status(), 202);
  assert.deepEqual(request.postDataJSON(), { workload: fixture.workload });
  await page.waitForURL(
    (url) => url.pathname === `/runs/${responseBody.run_id}`,
  );
}

async function authoringRoutesRetired(page) {
  // Arrange
  await page.goto(runsURL(), { waitUntil: "domcontentloaded" });
  await waitForRuns(page);
  const primary = page.getByRole("navigation", { name: "Primary" });

  // Act and assert
  for (const label of ["Create Run", "Preview", "Workloads"]) {
    assert.equal(
      await primary.getByRole("link", { name: label, exact: true }).count(),
      0,
    );
  }
  for (const retiredPath of [
    "/runs/new",
    "/preview",
    "/workloads",
    "/workloads/workload_fixture",
  ]) {
    await page.goto(runsURL(retiredPath), { waitUntil: "domcontentloaded" });
    await page.getByRole("heading", { name: "Page not found" }).waitFor();
    const returnLink = page.getByRole("link", { name: "Return to Workspace" });
    await returnLink.waitFor();
    assert.equal(
      await returnLink.getAttribute("href"),
      `/canvas?workspace_id=${workspaceID}`,
    );
  }
}

const desktop = await prepareContext({ width: 1280, height: 800 });
const page = await desktop.newPage();
recordBrowserFailures(page);
try {
  await localSessionSurvivesReload(page);
  await runsScopeCancelledCreate(page);
  await page
    .getByRole("button", { name: "Create run", exact: true })
    .first()
    .click();
  await page.screenshot({
    path: path.join(outputDirectory, "create-run-image.png"),
  });
  await page.keyboard.press("Escape");
  await minimalImageRunCreated(page);
  await page.screenshot({
    path: path.join(outputDirectory, "runs-navigation.png"),
  });
  await immutableSpecRunCreated(page);
  await authoringRoutesRetired(page);
} finally {
  await desktop.close();
}

const mobile = await prepareContext({ width: 390, height: 844 });
try {
  const mobilePage = await mobile.newPage();
  recordBrowserFailures(mobilePage);
  await mobilePage.goto(runsURL(), { waitUntil: "domcontentloaded" });
  await waitForRuns(mobilePage);
  await mobilePage
    .getByRole("button", { name: "Create run", exact: true })
    .first()
    .click();
  const dialog = mobilePage.getByRole("dialog", { name: "Create run" });
  await dialog.getByRole("tab", { name: "Spec" }).click();
  const box = await dialog.boundingBox();
  assert.ok(box);
  assert.ok(box.x >= 0 && box.width <= 390);
  await mobilePage.screenshot({
    path: path.join(outputDirectory, "create-run-spec-mobile.png"),
  });
  assert.deepEqual(browserFailures, []);
} finally {
  await mobile.close();
  await browser.close();
}

console.log(
  JSON.stringify({
    scenarios: [
      "runs_scope_cancelled_create",
      "local_session_survives_reload",
      "minimal_image_run_created",
      "immutable_spec_run_created",
      "authoring_routes_retired",
    ],
    screenshots: [
      "runs-navigation.png",
      "create-run-image.png",
      "create-run-spec-mobile.png",
    ],
  }),
);
