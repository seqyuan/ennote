import { expect, test, type Page, type Route } from "@playwright/test";

const now = "2026-08-08T00:00:00Z";

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

test("Models tab adds a provider, discovers and imports models, defaults and deletes", async ({ page }) => {
  const createdBodies: Record<string, unknown>[] = [];
  let provider: { id: string; name: string; baseUrl: string; apiKey: string } | null = null;
  let models: Array<{ id: string; providerId: string; modelName: string; displayName: string; contextWindow: number; isDefault: boolean }> = [];
  let defaultId: string | null = null;

  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, []);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/roles") return fulfill(route, { items: [], nextCursor: "" });
    if (path === "/v1/provider-profiles" && route.request().method() === "GET") {
      return fulfill(route, provider ? [provider] : []);
    }
    if (path === "/v1/provider-profiles" && route.request().method() === "POST") {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      createdBodies.push(body);
      provider = { id: "provider-1", name: String(body.name), baseUrl: String(body.baseUrl), apiKey: String(body.apiKey ?? "") };
      return fulfill(route, provider, 201);
    }
    if (path === "/v1/provider-profiles/discover-models") {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      expect(body).toEqual({ providerId: "provider-1" });
      return fulfill(route, [
        { modelName: "gpt-4o" },
        { modelName: "gpt-4o-mini" },
      ]);
    }
    if (path === "/v1/model-profiles" && route.request().method() === "GET") {
      return fulfill(route, models);
    }
    if (path === "/v1/model-profiles" && route.request().method() === "POST") {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      const model = {
        id: `model-${models.length + 1}`,
        providerId: String(body.providerId),
        modelName: String(body.modelName),
        displayName: String(body.displayName || body.modelName),
        contextWindow: Number(body.contextWindow),
        maxOutputTokens: Number(body.maxOutputTokens),
        inputCostUsdMicrosPerMillion: 0,
        outputCostUsdMicrosPerMillion: 0,
        supportsVision: false,
        supportsToolUse: true,
        supportsThinking: false,
        thinkingDialect: "none",
        supportedThinkingEfforts: [],
        isDefault: false,
        status: "active",
        createdAt: now,
        updatedAt: now,
      };
      models = [...models, model];
      return fulfill(route, model, 201);
    }
    const defaultMatch = path.match(/^\/v1\/model-profiles\/([^/]+)\/default$/);
    if (defaultMatch && route.request().method() === "PUT") {
      defaultId = defaultMatch[1];
      models = models.map(m => ({ ...m, isDefault: m.id === defaultId }));
      return route.fulfill({ status: 200 });
    }
    const modelDelete = path.match(/^\/v1\/model-profiles\/([^/]+)$/);
    if (modelDelete && route.request().method() === "DELETE") {
      models = models.filter(m => m.id !== modelDelete[1]);
      if (defaultId === modelDelete[1]) defaultId = null;
      return route.fulfill({ status: 204 });
    }
    if (path === "/v1/provider-profiles/provider-1" && route.request().method() === "DELETE") {
      provider = null;
      models = [];
      return route.fulfill({ status: 204 });
    }
    return route.abort();
  });

  await page.goto("/");
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "Settings" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Models" })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("tab", { name: "Providers", exact: true })).toHaveCount(0);

  // Add a provider with a plaintext key.
  await page.getByLabel("Name", { exact: true }).fill("openai-main");
  await page.getByLabel("Base URL", { exact: true }).fill("https://api.openai.com/v1");
  await page.getByLabel("API key", { exact: true }).fill("sk-test-123");
  await page.getByRole("button", { name: "Add provider", exact: true }).click();
  await expect(page.getByText("openai-main", { exact: true })).toBeVisible();
  await expect(page.getByText("https://api.openai.com/v1 · 0 models")).toBeVisible();
  expect(createdBodies).toEqual([{ name: "openai-main", providerType: "openai-compatible", baseUrl: "https://api.openai.com/v1", apiKey: "sk-test-123" }]);

  // Discover + import the catalog.
  await page.getByRole("button", { name: "Discover models" }).click();
  await expect(page.getByRole("dialog", { name: "Discover models" })).toBeVisible();
  await page.getByRole("button", { name: "Fetch catalog" }).click();
  await expect(page.getByText("gpt-4o", { exact: true })).toBeVisible();
  await expect(page.getByText("gpt-4o-mini", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Import 2 selected" }).click();
  await expect(page.getByRole("dialog", { name: "Discover models" })).toHaveCount(0);
  await expect(page.getByText("gpt-4o · 131,072 ctx · 0/0 uUSD/M · Tool")).toBeVisible();
  await expect(page.getByText("gpt-4o-mini · 131,072 ctx · 0/0 uUSD/M · Tool")).toBeVisible();

  // Make gpt-4o the default.
  await page.locator(".provider-settings-row .settings-row", { has: page.getByText("gpt-4o", { exact: true }) }).getByRole("button", { name: "Make default" }).click();
  await expect(page.locator(".provider-settings-row .settings-row", { has: page.getByText("gpt-4o", { exact: true }) }).getByRole("button", { name: "Default" })).toBeVisible();

  // Delete one model, then the provider.
  page.once("dialog", dialog => dialog.accept());
  await page.locator(".provider-settings-row .settings-row", { has: page.getByText("gpt-4o-mini", { exact: true }) }).getByLabel("Delete model gpt-4o-mini").click();
  await expect(page.getByText("gpt-4o-mini", { exact: true })).toHaveCount(0);

  page.once("dialog", dialog => dialog.accept());
  await page.getByLabel("Delete provider openai-main").click();
  await expect(page.getByText("openai-main", { exact: true })).toHaveCount(0);
  await expect(page.getByText("No providers yet")).toBeVisible();
});
