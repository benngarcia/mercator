import { expect, test } from "playwright/test";

const baseURL = process.env.MERCATOR_BROWSER_URL;
const token = process.env.MERCATOR_BROWSER_TOKEN;

test.skip(!baseURL || !token, "set MERCATOR_BROWSER_URL and MERCATOR_BROWSER_TOKEN");
test.use({ baseURL });

test("saved workspace chooser hides archives and creates the active workspace", async ({ page, request }) => {
  const suffix = Date.now().toString(36);
  const activeName = `Browser Active ${suffix}`;
  const archivedName = `Browser Archived ${suffix}`;
  const createdName = `Browser Created ${suffix}`;
  const headers = { Authorization: `Bearer ${token}` };

  const activeResponse = await request.post("/v1/workspaces", {
    headers,
    data: { display_name: activeName },
  });
  expect(activeResponse.status()).toBe(201);
  const archivedResponse = await request.post("/v1/workspaces", {
    headers,
    data: { display_name: archivedName },
  });
  expect(archivedResponse.status()).toBe(201);
  const archived = (await archivedResponse.json()) as { workspace: { id: string } };
  expect((await request.post(`/v1/workspaces/${archived.workspace.id}/archive`, { headers })).status()).toBe(200);

  await page.addInitScript((value) => {
    localStorage.setItem("mercator.token", value);
    localStorage.removeItem("mercator.workspace");
  }, token as string);
  await page.goto("/");
  await page.getByRole("combobox", { name: "Switch workspace" }).click();

  await expect(page.getByRole("option", { name: new RegExp(activeName) })).toBeVisible();
  await expect(page.getByText(archivedName)).toHaveCount(0);
  await page.getByRole("button", { name: "Show archived" }).click();
  await expect(page.getByRole("option", { name: new RegExp(archivedName) })).toBeVisible();
  if (process.env.MERCATOR_BROWSER_SCREENSHOT) {
    await page.getByRole("textbox", { name: "Search workspaces" }).fill(archivedName);
    await page.screenshot({
      path: process.env.MERCATOR_BROWSER_SCREENSHOT,
      fullPage: true,
    });
    await page.getByRole("textbox", { name: "Search workspaces" }).clear();
  }

  await page.getByRole("button", { name: "New workspace" }).click();
  await page.getByRole("textbox", { name: "Workspace name" }).fill(createdName);
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByRole("combobox", { name: "Switch workspace" })).toContainText(createdName);
});
