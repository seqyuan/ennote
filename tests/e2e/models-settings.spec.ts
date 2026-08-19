import { expect, test, type Route } from "@playwright/test";

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

test("Models tab: add custom provider, edit + fetch models, delete", async ({ page }) => {
  const createdProviders: Record<string, unknown>[] = [];
  const createdModels: Record<string, unknown>[] = [];
  let provider: { id: string; name: string; baseUrl: string; custom: boolean } | null = null;
  let models: Array<{ id: string; providerId: string; modelName: string; displayName: string; contextWindow: number; maxOutputTokens: number }> = [];

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
      createdProviders.push(body);
      provider = { id: String(body.key || body.name), name: String(body.name), baseUrl: String(body.baseUrl), custom: true };
      return fulfill(route, provider, 201);
    }
    if (path === "/v1/provider-profiles/discover-models") {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      expect(body.baseUrl).toBe("https://api.openai.com/v1");
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
      createdModels.push(body);
      const model = {
        id: `model-${models.length + 1}`,
        providerId: String(body.providerId),
        modelName: String(body.modelName),
        displayName: String(body.displayName || body.modelName),
        contextWindow: Number(body.contextWindow),
        maxOutputTokens: Number(body.maxOutputTokens),
      };
      models = [...models, model];
      return fulfill(route, model, 201);
    }
    // Provider + model updates are accepted no-op-ish.
    if (path === "/v1/provider-profiles/openai-main" && route.request().method() === "PUT") {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      if (provider) provider = { ...provider, name: String(body.name ?? provider.name), baseUrl: String(body.baseUrl ?? provider.baseUrl) };
      return fulfill(route, provider);
    }
    const modelUpdate = path.match(/^\/v1\/model-profiles\/([^/]+)$/);
    if (modelUpdate && route.request().method() === "PUT") {
      return route.fulfill({ status: 200 });
    }
    if (path === "/v1/provider-profiles/openai-main" && route.request().method() === "DELETE") {
      provider = null;
      models = [];
      return route.fulfill({ status: 204 });
    }
    return route.abort();
  });

  await page.goto("/");
  // The e2e storage state disables first-run guidance (it would intercept
  // clicks across the suite), so open the dialog through the trigger and
  // land on the Models tab explicitly.
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "Settings" })).toBeVisible();
  await page.getByRole("tab", { name: "Models" }).click();
  await expect(page.getByRole("tab", { name: "Models" })).toHaveAttribute("aria-selected", "true");

  // Add a custom provider with one model.
  await page.getByRole("button", { name: "Add a custom provider" }).click();
  await page.getByLabel("Provider ID", { exact: true }).fill("openai-main");
  await page.getByLabel("Base URL", { exact: true }).fill("https://api.openai.com/v1");
  await page.getByLabel("API key", { exact: true }).fill("sk-test-123");
  await page.getByRole("button", { name: "Add model" }).click();
  await page.getByLabel("Model ID 1").fill("gpt-4o");
  await page.getByRole("button", { name: "Create provider" }).click();
  // The provider row renders (name + Custom tag), so assert its actions.
  await expect(page.getByRole("button", { name: "Edit openai-main" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Delete openai-main" })).toBeVisible();
  expect(createdProviders[0]).toEqual({
    key: "openai-main",
    name: "openai-main",
    providerType: "openai-compatible",
    baseUrl: "https://api.openai.com/v1",
    apiKey: "sk-test-123",
  });
  expect(createdModels).toHaveLength(1);

  // Edit: open the custom-settings fold, fetch the catalog into the draft, apply.
  await page.getByRole("button", { name: "Edit openai-main" }).click();
  await page.getByText("Customized settings").click();
  await page.getByRole("button", { name: "Fetch available models" }).click();
  await expect(page.getByRole("dialog", { name: "Choose models to add" })).toBeVisible();
  await page.getByRole("button", { name: "Add selected" }).click();
  await page.getByRole("button", { name: "Apply" }).click();
  // Apply is async; the editor closes only after it succeeds and refreshes.
  await expect(page.getByRole("button", { name: "Apply" })).toHaveCount(0);
  // gpt-4o-mini was adopted as a new model; gpt-4o unchanged.
  expect(createdModels.some(m => m.modelName === "gpt-4o-mini")).toBe(true);

  // Delete the provider through the confirmation modal; the row disappears.
  await page.getByLabel("Delete openai-main").click();
  await expect(page.getByRole("dialog", { name: "Delete openai-main?" })).toBeVisible();
  await page.getByRole("dialog", { name: "Delete openai-main?" }).getByRole("button", { name: "Delete openai-main" }).click();
  await expect(page.getByRole("button", { name: "Edit openai-main" })).toHaveCount(0);
});
