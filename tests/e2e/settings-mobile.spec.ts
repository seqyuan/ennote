import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * Mobile settings dialog (≤640px) renders as a full-screen sheet: top bar
 * with the current-section title + close, a horizontally scrollable pill
 * tab strip, and a panel that fills the remaining height.
 *
 * Regresses the cascade bug where app/settings.css (loaded after
 * globals.css) silently defeated the globals mobile media query, leaving
 * the desktop 188px vertical nav rail (with 132px-square tabs) on phones.
 */

async function fulfill(route: Route, data: unknown, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockApp(page: Page) {
  await page.route("**/api/worker/v1/**", async route => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, []);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/provider-profiles") return fulfill(route, []);
    if (path === "/v1/model-profiles") return fulfill(route, []);
    if (path === "/v1/roles") return fulfill(route, { items: [], nextCursor: "" });
    return route.abort();
  });
}

test.describe("mobile settings dialog", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("renders as a full-screen sheet with a horizontal pill tab strip", async ({ page }) => {
    await mockApp(page);
    // No usable provider → first-run guidance opens the Models section.
    await page.goto("/");
    const shell = page.locator(".settings-dialog-shell");
    await expect(shell).toBeVisible();

    // Full-screen sheet: no rounded card margins on phones.
    const box = await shell.boundingBox();
    const viewport = page.viewportSize()!;
    expect(box!.width).toBe(viewport.width);
    expect(box!.height).toBe(viewport.height);

    // Top bar: current-section title + close button above the tab strip.
    const title = page.locator(".settings-dialog-current");
    await expect(title).toHaveText("Models");
    const titleBox = await title.boundingBox();
    const closeBox = await page.getByRole("button", { name: "Close settings" }).boundingBox();
    const stripBox = await page.locator(".settings-dialog-tabs").boundingBox();
    expect(Math.max(titleBox!.y, closeBox!.y)).toBeLessThan(stripBox!.y);

    // Tab strip is a horizontal row of 40px pills docked at the sheet
    // bottom — not the desktop vertical rail with 132px-square tabs.
    await expect(page.locator(".settings-dialog-tabs")).toHaveCSS("flex-direction", "row");
    const tabBox = await page.getByRole("tab", { name: "Models" }).boundingBox();
    expect(Math.round(tabBox!.height)).toBe(40);
    const dockedStrip = await page.locator(".settings-dialog-tabs").boundingBox();
    expect(Math.round(dockedStrip!.y + dockedStrip!.height)).toBe(viewport.height);

    // Switching sections updates the top-bar title and the panel.
    await page.getByRole("tab", { name: "General" }).click();
    await expect(title).toHaveText("General");
    await expect(page.getByRole("heading", { name: "General", level: 2 })).toBeVisible();

    // Every tab is reachable by scrolling the strip (last tab included).
    const skills = page.getByRole("tab", { name: "Skills" });
    await skills.scrollIntoViewIfNeeded();
    await skills.click();
    await expect(title).toHaveText("Skills");
    await expect(shell).toBeVisible();

    // Close returns to the app shell.
    await page.getByRole("button", { name: "Close settings" }).click();
    await expect(shell).not.toBeVisible();
  });
});
