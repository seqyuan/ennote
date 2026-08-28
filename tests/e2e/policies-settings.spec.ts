import { expect, test, type Route } from "@playwright/test";

const now = "2026-08-07T00:00:00Z";
const policy = {
  id: "tool-policy-1",
  name: "Restricted local tools",
  kind: "tool",
  version: 1,
  config: { mode: "restricted" },
  status: "active",
  createdAt: now,
  updatedAt: now,
};

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

test("active policy profiles can be deactivated", async ({ page }) => {
  let active = true;
  await page.route("**/api/worker/v1/**", async route => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (path === "/v1/projects" || path === "/v1/provider-profiles" || path === "/v1/model-profiles") {
      return fulfill(route, []);
    }
    if (path === "/v1/policy-profiles") return fulfill(route, active ? [policy] : []);
    if (path === `/v1/policy-profiles/${policy.id}` && route.request().method() === "DELETE") {
      active = false;
      return route.fulfill({ status: 204 });
    }
    return route.abort();
  });

  await page.goto("/");
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByRole("tab", { name: /Policies/ }).click();
  await expect(page.getByText(policy.name)).toBeVisible();

  page.once("dialog", dialog => dialog.accept());
  await page.getByLabel(`Deactivate policy ${policy.name} version ${policy.version}`).click();
  await expect(page.getByText(policy.name)).toHaveCount(0);
  expect(active).toBe(false);
});
