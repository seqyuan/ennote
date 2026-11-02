import { expect, test } from "@playwright/test";

test("announces read-only state while the browser is offline", async ({ page, context }) => {
  await page.goto("/");
  await expect(page.getByTestId("ennote-shell")).toBeVisible();

  const banner = page.getByTestId("offline-banner");
  await expect(banner).toHaveCount(0);

  // navigator.onLine is the same signal apiResponse refuses mutations on, so the
  // banner and the guard cannot disagree.
  await context.setOffline(true);
  await expect(banner).toContainText("Offline: Ennote is read-only until the connection returns.");

  await context.setOffline(false);
  await expect(banner).toHaveCount(0);
});
