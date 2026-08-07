import { expect, test, type Page, type Route } from "@playwright/test";

const now = "2026-08-05T00:00:00Z";
const project = { id: "mcp-project", name: "RNA screen", description: "", status: "active", createdAt: now, updatedAt: now };
const profile = { id: "pubmed-profile", displayName: "PubMed", slug: "pubmed", sourceKind: "managed",
  projectScope: null, sourceLocator: "", lifecycleStatus: "active", createdAt: now, updatedAt: now, latestVersion: 1 };
const version = { id: "pubmed-v1", profileId: profile.id, version: 1, transport: "stdio",
  executable: "uvx", argv: ["mcp-server-pubmed"], endpoint: "", envLiterals: {}, envCredentials: { API_KEY: "env:PUBS_KEY" },
  headerLiterals: {}, headerCredentials: {}, cwd: "", timeoutMs: 15000, networkPolicy: "default",
  configDigest: "sha256:" + "a".repeat(64), createdAt: now };
const catalog = [
  { remoteName: "search_articles", exposedName: "pubmed__search_articles", description: "Search PubMed articles",
    inputSchema: { type: "object" }, outputSchema: null, readOnlyHint: true, digest: "d1" },
  { remoteName: "get_article", exposedName: "pubmed__get_article", description: "Fetch one article",
    inputSchema: { type: "object" }, outputSchema: null, readOnlyHint: true, digest: "d2" },
];
const candidate = { slug: "pubmed", displayName: "PubMed", sourceKind: "project_file", sourceLocator: ".ennote/mcp.json",
  transport: "stdio", executable: "uvx", endpoint: "", configDigest: "sha256:" + "b".repeat(64), alreadyBound: false };
const binding = { id: "binding-1", projectId: project.id, profileVersionId: version.id, desiredEnabled: false,
  required: true, selectedRemoteToolNames: [], credentialRefs: {}, revision: 1, createdAt: now, updatedAt: now };

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockMCP(page: Page) {
  // Stateful mock: binding list evolves as the UI creates/enables it.
  const bindings: Array<typeof binding> = [];
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, []);
    if (path === "/v1/mcp/server-profiles") return fulfill(route, [profile]);
    if (path === `/v1/projects/${project.id}/mcp/candidates`) return fulfill(route, [candidate]);
    if (path === `/v1/projects/${project.id}/mcp/bindings`) {
      if (route.request().method() === "GET") return fulfill(route, bindings);
      if (route.request().method() === "POST") {
        const created = { ...binding, desiredEnabled: false };
        bindings.push(created);
        return fulfill(route, created, 201);
      }
      return route.abort();
    }
    if (path === `/v1/projects/${project.id}/mcp/bindings/from-candidate`) {
      const created = { ...binding, desiredEnabled: false };
      bindings.push(created);
      return fulfill(route, created, 201);
    }
    if (path.includes("/bindings/")) {
      // PATCH /bindings/{id}: flip enabled state, selection, required, and refs.
      if (route.request().method() === "PATCH") {
        const target = bindings[0];
        if (!target) return fulfill(route, { code: "not_found", message: "not found" }, 404);
        const body = JSON.parse(route.request().postData() ?? "{}");
        if (typeof body.desiredEnabled === "boolean") target.desiredEnabled = body.desiredEnabled;
        if (Array.isArray(body.selectedRemoteToolNames)) target.selectedRemoteToolNames = body.selectedRemoteToolNames;
        if (typeof body.required === "boolean") target.required = body.required;
        if (typeof body.credentialRefs === "object" && body.credentialRefs !== null) target.credentialRefs = body.credentialRefs;
        target.revision += 1;
        return fulfill(route, target);
      }
    }
    if (path.endsWith("/catalog/refresh") || path.endsWith("/catalog")) return fulfill(route, catalog);
    if (path.endsWith("/test")) return fulfill(route, { ok: true, transport: "stdio" });
    return route.abort();
  });
}

async function selectProjectAndOpenMCPSettings(page: Page) {
  await page.goto("/");
  if ((page.viewportSize()?.width ?? 1280) <= 640) await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByTitle("Select project").click();
  await page.getByRole("button", { name: project.name }).click();
  await page.getByRole("button", { name: "Open settings" }).click();
  await page.getByRole("tab", { name: /MCP/ }).click();
}

for (const viewport of [{ width: 1280, height: 800 }, { width: 390, height: 844 }]) {
  test(`MCP settings discovers, binds, and enables at ${viewport.width}x${viewport.height}`, async ({ page }) => {
    await page.setViewportSize(viewport);
    // The UI shows a security confirmation before enabling a stdio server;
    // accept it so the enable proceeds.
    page.on("dialog", (dialog) => void dialog.accept());
    await mockMCP(page);
    await selectProjectAndOpenMCPSettings(page);

    // Discovered server from the project file appears as a candidate.
    await expect(page.getByText("Discovered servers")).toBeVisible();
    await expect(page.getByText("PubMed")).toBeVisible();
    await expect(page.getByText("stdio · project_file")).toBeVisible();

    // Bind the candidate -> binding appears as disabled with zero tools.
    await page.getByRole("button", { name: "Bind", exact: true }).click();
    await expect(page.getByText("Disabled")).toBeVisible();
    await expect(page.getByText("0 tools selected")).toBeVisible();

    // Enable -> tool catalog loads; select a tool by checkbox.
    await page.getByRole("button", { name: "Disabled" }).click();
    await expect(page.getByText("Enabled")).toBeVisible();
    // Enable triggers a refresh that loads the tool catalog.
    const checkbox = page.locator('input[type="checkbox"]').first();
    await expect(checkbox).toBeVisible({ timeout: 5000 });
    await checkbox.click();
    await expect(page.getByText("1 tools selected")).toBeVisible({ timeout: 5000 });

    // Test connection button exists and is clickable.
    await page.getByRole("button", { name: "Test", exact: true }).click();

    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
    expect(overflow).toBe(false);
  });
}

test("MCP settings shows Bound + Update available for a stale project-file candidate", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  const staleCandidate = { ...candidate, alreadyBound: true, boundVersionId: version.id, updateAvailable: true };
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, []);
    if (path === "/v1/mcp/server-profiles") return fulfill(route, [profile]);
    if (path === `/v1/projects/${project.id}/mcp/candidates`) return fulfill(route, [staleCandidate]);
    if (path === `/v1/projects/${project.id}/mcp/bindings`) return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/mcp/bindings/from-candidate`) return fulfill(route, binding, 201);
    return route.abort();
  });
  await selectProjectAndOpenMCPSettings(page);

  await page.getByText("Discovered servers").scrollIntoViewIfNeeded();
  await expect(page.getByText("Bound", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Update available" })).toBeVisible();
  await page.getByRole("button", { name: "Update available" }).click();
});

test("MCP settings toggles required, edits credentials, and searches tools", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  page.on("dialog", (dialog) => void dialog.accept());
  await mockMCP(page);
  await selectProjectAndOpenMCPSettings(page);

  // Bind + enable.
  await page.getByRole("button", { name: "Bind", exact: true }).click();
  await page.getByRole("button", { name: "Disabled" }).click();
  await expect(page.getByText("Enabled")).toBeVisible();

  // Toggle required -> optional (button switches and persists).
  await page.getByRole("button", { name: "required", exact: true }).click();
  await expect(page.getByRole("button", { name: "optional", exact: true })).toBeVisible();

  // Search narrows the tool table.
  await page.getByPlaceholder("Search tools…").fill("get_article");
  await expect(page.getByText("get_article")).toBeVisible();
  await expect(page.getByText("search_articles")).toHaveCount(0);

  // Credentials editor adds a binding-level ref (refs only).
  await page.getByRole("button", { name: "Credentials" }).click();
  await page.getByPlaceholder("ENV_NAME").fill("NCBI_API_KEY");
  await page.getByPlaceholder("env:MY_REF / file:... / keyring:...").fill("env:NCBI_KEY");
  await page.getByRole("button", { name: "Add", exact: true }).click();
  await expect(page.getByText("env:NCBI_KEY")).toBeVisible();
});

// Profile diff: when a project file changes, the UI shows a read-only diff
// between the bound (frozen) version and the new candidate connection fields.
test("update-available candidate exposes a read-only profile diff", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  const staleCandidate = { slug: "pubmed", displayName: "PubMed", sourceKind: "project_file",
    sourceLocator: ".ennote/mcp.json", transport: "stdio", executable: "python3", endpoint: "",
    configDigest: "sha256:" + "c".repeat(64), boundVersionId: version.id, alreadyBound: true, updateAvailable: true };
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, []);
    if (path === "/v1/mcp/server-profiles") return fulfill(route, [profile]);
    if (path === `/v1/mcp/server-profiles/${profile.id}/versions`) return fulfill(route, [version]);
    if (path === `/v1/projects/${project.id}/mcp/candidates`) return fulfill(route, [staleCandidate]);
    if (path === `/v1/projects/${project.id}/mcp/bindings`) return fulfill(route, [binding]);
    return route.abort();
  });
  await selectProjectAndOpenMCPSettings(page);

  await expect(page.getByText("Update available")).toBeVisible();
  await page.getByRole("button", { name: "View diff" }).click();
  await expect(page.getByText("Read-only diff: bound version vs project file")).toBeVisible();
  // Bound executable (uvx, removed) vs candidate executable (python3, added).
  await expect(page.getByText(/"executable": "uvx"/)).toBeVisible();
  await expect(page.getByText(/"executable": "python3"/)).toBeVisible();
  // New config digest is surfaced.
  await expect(page.getByText(/new config sha256:/)).toBeVisible();
});
