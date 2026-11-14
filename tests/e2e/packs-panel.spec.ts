import { expect, test, type Page, type Route } from "@playwright/test";
import { tryFulfillHarnessRoutes } from "./harness";

/**
 * The Agent Packs panel.
 *
 * A real pack install writes store objects, so these tests mock the Worker's
 * answers: what matters here is that the panel shows the Worker's own verdict —
 * the plan, its actions, its blockers — and that it does not offer the install
 * while a blocker is present.
 */

const project = { id: "pack-project", name: "Pack project", description: "", status: "active", createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-01T00:00:00Z" };
const policies = ["discuss", "ask", "auto"].map((mode) => ({ id: `builtin-tool-${mode}-v1`, name: mode, kind: "tool", version: 1, config: { mode }, status: "active", createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-01T00:00:00Z" }));
const provider = { id: "pack-provider", name: "Pack provider", providerType: "openai-compatible", baseUrl: "https://example.test", credentialConfigured: true, status: "active", createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-01T00:00:00Z" };

const packIdentity = { id: "acme/reviewer", name: "reviewer", version: "1.2.0" };
const inspectResult = {
  inspectVersion: 1,
  source: { kind: "path", path: "/home/me/acme.enpack" },
  container: "directory",
  schemaVersion: 1,
  pack: packIdentity,
  manifestDigest: "a".repeat(64),
  packDigest: "b".repeat(64),
  signature: { kind: "none" },
  signed: false,
  flows: [],
  heroes: [{ handle: "reviewer" }],
  skills: [{ id: "review" }],
  mcp: [],
  requires: [],
  capabilityEnvelope: {},
  files: [],
  filesTotal: 3,
  filesTruncated: false,
  warnings: [{ code: "unsigned", severity: "warning", message: "This pack is not signed." }],
  manifest: {},
};

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockPacks(page: Page, plan: { blocked: boolean }) {
  let installed: unknown[] = [];
  await page.route("**/api/worker/v1/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, []);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, []);
    if (path === "/v1/packs" && request.method() === "GET") return fulfill(route, installed);
    if (path === "/v1/packs/inspect") return fulfill(route, inspectResult);
    if (path === "/v1/packs/install") {
      await fulfill(route, {
        planVersion: 1,
        pack: packIdentity,
        manifestDigest: "a".repeat(64),
        packDigest: "b".repeat(64),
        actions: [
          { kind: "hero", storeId: "reviewer", action: "create", scope: "global" },
          { kind: "skill", storeId: "review", action: "create" },
        ],
        blockers: plan.blocked
          ? [{ code: "conflict", severity: "error", message: "hero reviewer already exists" }]
          : [],
        warnings: [],
        commitPoint: "after-staging",
            });
      installed = [{ pack: packIdentity, manifestDigest: "a".repeat(64), packDigest: "b".repeat(64), installedAt: "2026-11-01T00:00:00Z", installState: "installed_draft", source: inspectResult.source, flowCount: 0, heroCount: 1, skillCount: 1, externalRefs: {} }];
      return;
    }
    if (path.startsWith("/v1/packs/") && request.method() === "DELETE") {
      installed = [];
      return route.fulfill({ status: 204, body: "" });
    }
    return route.abort();
  });
}

async function openPacksTab(page: Page) {
  await page.goto("/");
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByRole("tab", { name: /Packs/ }).click();
}

test("a pack is inspected and planned before it can be installed", async ({ page }) => {
  await mockPacks(page, { blocked: false });
  await openPacksTab(page);

  const install = page.getByRole("button", { name: "Install", exact: true });
  await expect(install).toBeDisabled();

  await page.getByLabel("Pack path on the Worker host").fill("/home/me/acme.enpack");
  await page.getByRole("button", { name: "Inspect" }).click();

  const inspected = page.getByTestId("pack-inspect");
  await expect(inspected).toContainText("reviewer");
  await expect(inspected).toContainText("1 heroes · 1 skills");
  await expect(inspected).toContainText("unsigned");
  await expect(install).toBeDisabled(); // inspected, but not planned yet

  await page.getByRole("button", { name: "Check install" }).click();
  await expect(page.getByTestId("pack-plan")).toContainText("2 actions");
  await expect(install).toBeEnabled();

  await install.click();
  await page.getByRole("alertdialog", { name: /Install reviewer/ }).getByRole("button", { name: "Install" }).click();
  await expect(page.getByTestId("pack-row")).toContainText("reviewer");
  await expect(page.getByTestId("pack-row")).toContainText("1 heroes");
});

test("a blocked plan is shown and the install stays unavailable", async ({ page }) => {
  await mockPacks(page, { blocked: true });
  await openPacksTab(page);

  await page.getByLabel("Pack path on the Worker host").fill("/home/me/acme.enpack");
  await page.getByRole("button", { name: "Check install" }).click();

  const plan = page.getByTestId("pack-plan");
  await expect(plan).toHaveAttribute("data-blocked", "true");
  await expect(plan).toContainText("hero reviewer already exists");
  await expect(page.getByRole("button", { name: "Install", exact: true })).toBeDisabled();
});

test("changing the path voids the plan that was approved for the old one", async ({ page }) => {
  await mockPacks(page, { blocked: false });
  await openPacksTab(page);

  await page.getByLabel("Pack path on the Worker host").fill("/home/me/acme.enpack");
  await page.getByRole("button", { name: "Check install" }).click();
  const install = page.getByRole("button", { name: "Install", exact: true });
  await expect(install).toBeEnabled();

  await page.getByLabel("Pack path on the Worker host").fill("/home/me/other.enpack");
  await expect(install).toBeDisabled();
  await expect(page.getByTestId("pack-plan")).toHaveCount(0);
});

test("removing a pack asks first", async ({ page }) => {
  await mockPacks(page, { blocked: false });
  await openPacksTab(page);
  await page.getByLabel("Pack path on the Worker host").fill("/home/me/acme.enpack");
  await page.getByRole("button", { name: "Check install" }).click();
  await page.getByRole("button", { name: "Install", exact: true }).click();
  await page.getByRole("alertdialog", { name: /Install reviewer/ }).getByRole("button", { name: "Install" }).click();
  await expect(page.getByTestId("pack-row")).toBeVisible();

  await page.getByRole("button", { name: "Remove reviewer" }).click();
  const confirm = page.getByRole("alertdialog", { name: /Remove reviewer/ });
  await expect(confirm).toBeVisible();
  await confirm.getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByTestId("pack-row")).toBeVisible();

  await page.getByRole("button", { name: "Remove reviewer" }).click();
  await page.getByRole("alertdialog", { name: /Remove reviewer/ }).getByRole("button", { name: "Remove" }).click();
  await expect(page.getByTestId("pack-row")).toHaveCount(0);
});
