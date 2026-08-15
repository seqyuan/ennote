import { expect, test, type Page, type Route } from "@playwright/test";

const project = { id: "artifact-project", name: "Artifact study", description: "", status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };
const session = { id: "artifact-session", projectId: project.id, title: "Scientific outputs", status: "active", activeLeafMessageId: "m4", activeBranchId: "main", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:04Z" };
const policies = ["discuss", "ask", "auto"].map(mode => ({ id: `builtin-tool-${mode}-v1`, name: mode, kind: "tool", version: 1, config: { mode }, status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" }));

const references = [
  { artifactId: "image", name: "umap.png", kind: "image", mimeType: "image/png", sizeBytes: 68, sha256: "image-sha", width: 1, height: 1 },
  { artifactId: "csv", name: "markers.csv", kind: "table", mimeType: "text/csv; charset=utf-8", sizeBytes: 420, sha256: "csv-sha" },
  { artifactId: "xlsx", name: "workbook.xlsx", kind: "table", mimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", sizeBytes: 2048, sha256: "xlsx-sha" },
  { artifactId: "html", name: "report.html", kind: "static_html", mimeType: "text/html; charset=utf-8", sizeBytes: 1024, sha256: "html-sha" },
  { artifactId: "stdout", name: "stdout.txt", kind: "text", mimeType: "text/plain; charset=utf-8", sizeBytes: 120, sha256: "text-sha" },
  { artifactId: "h5ad", name: "a-very-long-single-cell-analysis-result-that-must-not-expand-the-conversation-width.h5ad", kind: "file", mimeType: "application/octet-stream", sizeBytes: 4096, sha256: "file-sha" },
] as const;

const messages = [
  message("m1", undefined, "user", [{ type: "text", text: "Publish the scientific outputs." }]),
  message("m2", "m1", "assistant", [{ type: "tool_call", toolCall: { id: "publish", name: "publish_artifact", arguments: { path: "/workspace/results" } } }]),
  message("m3", "m2", "tool", [{ type: "tool_result", toolResult: { toolCallId: "publish", toolName: "publish_artifact", content: "Published six artifacts", isError: false, artifacts: references } }]),
  message("m4", "m3", "assistant", [{ type: "text", text: "The result files are ready." }]),
];

function message(id: string, parentMessageId: string | undefined, role: "user" | "assistant" | "tool", parts: unknown[]) {
  return { id, sessionId: session.id, parentMessageId, role, status: "complete", parts, createdAt: `2026-07-28T00:00:0${id.slice(-1)}Z` };
}

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockArtifacts(page: Page) {
  await page.route("https://artifact.invalid/**", route => route.abort());
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages, hasMore: false, activeLeafMessageId: "m4" });
    if (path === `/v1/sessions/${session.id}/compactions`) return fulfill(route, []);
    if (path === `/v1/sessions/${session.id}/branches`) return fulfill(route, [{ id: "main", sessionId: session.id, label: "Main", leafMessageId: "m4", messageCount: 4, active: true, createdAt: session.createdAt, updatedAt: session.updatedAt }]);
    if (path === `/v1/sessions/${session.id}/recovery`) return fulfill(route, null);
    const match = path.match(new RegExp(`^/v1/sessions/${session.id}/artifacts/([^/]+)/(preview|download)$`));
    if (!match) return route.abort();
    const [, artifactId, action] = match;
    if (action === "download") {
      return route.fulfill({ status: 200, headers: { "Content-Type": "application/octet-stream", "Content-Disposition": `attachment; filename="${artifactId}.bin"` }, body: `download-${artifactId}` });
    }
    if (artifactId === "image") {
      return route.fulfill({ status: 200, contentType: "image/png", body: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=", "base64") });
    }
    if (artifactId === "csv") return fulfill(route, { format: "csv", columns: ["gene", "cluster"], rows: [["EPCAM", "C1"], ["KRT8", "C2"]], truncatedRows: true, truncatedColumns: false, rowLimit: 100, columnLimit: 30 });
    if (artifactId === "xlsx") {
      const selected = url.searchParams.get("sheet") || "Case";
      return fulfill(route, { format: "xlsx", sheets: ["Case", "Control"], sheet: selected, columns: ["sample", "value"], rows: [[selected === "Case" ? "case-1" : "control-1", "7"]], truncatedRows: false, truncatedColumns: false, rowLimit: 100, columnLimit: 30 });
    }
    if (artifactId === "stdout") return fulfill(route, { text: "complete command output\nline two", truncated: false });
    if (artifactId === "html") {
      return route.fulfill({ status: 200, contentType: "text/html", headers: {
        "Content-Security-Policy": "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:; form-action 'none'; base-uri 'none'",
      }, body: `<h1>Static report</h1><script>parent.document.body.dataset.compromised='yes'</script><img src="https://artifact.invalid/tracker.png"><form action="https://artifact.invalid/post"><button>submit</button></form>` });
    }
    return route.fulfill({ status: 415, contentType: "application/json", body: JSON.stringify({ error: { code: "artifact_preview_unsupported", message: "unsupported" } }) });
  });
}

async function openArtifacts(page: Page) {
  await mockArtifacts(page);
  await page.goto("/");
  if ((page.viewportSize()?.width ?? 1280) <= 640) await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByTitle("Select project").first().click();
  await page.getByLabel("Projects", { exact: true }).getByRole("button", { name: project.name }).click();
  if ((page.viewportSize()?.width ?? 1280) <= 640) await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByRole("button", { name: session.title, exact: true }).click();
  await expect(page.locator("[data-artifact-id]" )).toHaveCount(6);
}

test("renders typed scientific previews and keeps HTML isolated", async ({ page }) => {
  await openArtifacts(page);
  const image = page.locator('[data-artifact-id="image"] img');
  await expect(image).toBeVisible();
  await expect.poll(() => image.evaluate(element => (element as HTMLImageElement).naturalWidth)).toBe(1);
  await expect(page.locator('[data-artifact-id="csv"] table')).toContainText("EPCAM");
  await expect(page.locator('[data-artifact-id="csv"]')).toContainText("Preview limited to 100 rows x 30 columns");
  await expect(page.locator('[data-artifact-id="stdout"] pre')).toContainText("complete command output");

  const workbook = page.locator('[data-artifact-id="xlsx"]');
  await expect(workbook.getByRole("cell", { name: "case-1" })).toBeVisible();
  await workbook.getByRole("combobox", { name: "Worksheet" }).selectOption("Control");
  await expect(workbook.getByRole("cell", { name: "control-1" })).toBeVisible();

  const frame = page.locator('[data-artifact-id="html"] iframe');
  await expect(frame).toHaveAttribute("sandbox", "");
  await expect(frame).toHaveAttribute("referrerpolicy", "no-referrer");
  await expect(frame.contentFrame().getByRole("heading", { name: "Static report" })).toBeVisible();
  await page.waitForTimeout(100);
  await expect(page.locator("body")).not.toHaveAttribute("data-compromised", "yes");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
  await page.locator(".messages").evaluate(element => { element.scrollTop = 0; });
  await page.screenshot({ path: "/tmp/ennote-artifact-results-desktop.png", fullPage: true });
});

test("downloads immutable artifacts through the Session-scoped route", async ({ page }) => {
  await openArtifacts(page);
  const link = page.locator('[data-artifact-id="h5ad"] .artifact-download');
  await expect(link).toHaveAttribute("href", `/api/worker/v1/sessions/${session.id}/artifacts/h5ad/download`);
  await expect(link).toHaveAttribute("download", references[5].name);
});

test.describe("mobile", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test("wide tables and long artifact names stay inside the viewport", async ({ page }) => {
    await openArtifacts(page);
    await expect(page.locator('[data-artifact-id="h5ad"]')).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
    await page.locator(".messages").evaluate(element => { element.scrollTop = 0; });
    await page.screenshot({ path: "/tmp/ennote-artifact-results-mobile.png", fullPage: true });
  });
});
