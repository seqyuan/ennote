import { expect, test } from "@playwright/test";

test("announces read-only state when the Worker stops answering", async ({ page, context }) => {
  await page.goto("/");
  await expect(page.getByTestId("ennote-shell")).toBeVisible();

  const banner = page.getByTestId("offline-banner");
  await expect(banner).toHaveCount(0);

  // Taking the browser offline is what makes the Worker unreachable here, so the
  // probe fails and the banner appears. navigator.onLine only triggers the
  // probe — the read-only verdict comes from the failed exchange itself, which
  // is why a machine with no network but a live loopback Worker keeps its write
  // path (pinned in tests/unit/worker-api-client.test.ts).
  await context.setOffline(true);
  await expect(banner).toContainText("Worker unreachable");

  await context.setOffline(false);
  await expect(banner).toHaveCount(0);
});
