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

test("announces read-only state when a response arrives without the Worker", async ({ page }) => {
  // What this pins is the client's rule, not a system state: ennogate answers
  // 502 without the Worker instance header when it cannot reach the process,
  // and the App must read that as "the Worker never spoke" even though the
  // socket answered. A response with no header is the discriminator (see
  // tests/unit/worker-api-client.test.ts); this proves the rule reaches the UI.
  await page.route("**/api/worker/v1/health/live", (route) =>
    route.fulfill({ status: 502, contentType: "text/plain", body: "bad gateway" }),
  );

  await page.goto("/");
  const banner = page.getByTestId("offline-banner");
  await expect(banner).toContainText("Worker unreachable");

  // Recovery goes through the monitor's own unreachable cadence, with no
  // synthetic event: the App is supposed to notice on its next probe.
  await page.unroute("**/api/worker/v1/health/live");
  await expect(banner).toHaveCount(0, { timeout: 15_000 });
});
