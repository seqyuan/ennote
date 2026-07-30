import { expect, test } from "@playwright/test";

async function openCompactionSettings(page: import("@playwright/test").Page) {
  await page.goto("/");
  await page.getByTestId("ennote-shell").waitFor();
  if ((page.viewportSize()?.width ?? 1280) <= 640) await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByRole("tab", { name: "Context & session" }).click();
  await expect(page.getByRole("heading", { name: "Context compaction", exact: true })).toBeVisible();
  await expect(page.locator('select[name="mode"]')).toHaveValue("manual_only");
  await expect(page.locator('select[name="compactionModelProfileId"]')).toBeVisible();
  await expect(page.getByLabel("History lookup", { exact: true })).toBeChecked();
  await expect(page.getByLabel("Overflow recovery", { exact: true })).toBeChecked();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
}

test("context compaction settings expose the frozen policy controls", async ({ page }) => {
  await openCompactionSettings(page);
});

test.describe("mobile", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("context compaction settings remain within the viewport", async ({ page }) => {
    await openCompactionSettings(page);
    await page.getByRole("heading", { name: "Context compaction", exact: true }).scrollIntoViewIfNeeded();
    await expect(page.locator('input[name="triggerRatio"]')).toBeVisible();
  });
});
