import { expect, test, type Page, type Route } from "@playwright/test";
import { openSession, tryFulfillHarnessRoutes } from "./harness";

const now = "2026-08-07T00:00:00Z";
const later = "2026-08-07T00:10:00Z";
const latest = "2026-08-07T00:20:00Z";
const project = { id: "accept-proj", name: "Acceptance", description: "", status: "active", createdAt: now, updatedAt: now };
const session = { id: "accept-sess", projectId: project.id, title: "Acceptance session", status: "active",
  activeLeafMessageId: "m1", createdAt: now, updatedAt: now };

// A completed Run whose clause could not be fully decided: the amber marker is the
// only thing that separates it from a clean Run.
const unverifiedRun = { runId: "run-unverified", sessionId: session.id, projectId: project.id, flowVersionId: "v1",
  manifestDigest: "a".repeat(64), state: "completed", totalTokensUsed: 100, inputs: { name: "Checked flow" },
  acceptance: { total: 2, holds: 1, doesNotHold: 0, unverified: 1 },
  createdAt: latest, updatedAt: latest };
// A Run that did not hold already fails, so its card must not repeat the signal.
const failedRun = { runId: "run-failed", sessionId: session.id, projectId: project.id, flowVersionId: "v1",
  manifestDigest: "b".repeat(64), state: "failed", totalTokensUsed: 60, inputs: { name: "Missed flow" },
  acceptance: { total: 1, holds: 0, doesNotHold: 1, unverified: 0 },
  createdAt: later, updatedAt: later };
// No `acceptance` key at all: a Run whose tasks declared nothing.
const plainRun = { runId: "run-plain", sessionId: session.id, projectId: project.id, flowVersionId: "v1",
  manifestDigest: "c".repeat(64), state: "completed", totalTokensUsed: 20, inputs: { name: "Plain flow" },
  createdAt: now, updatedAt: now };

const unverifiedNodes = [
  { runId: unverifiedRun.runId, taskIndex: 0, handle: "collector", terminalState: "completed", goalText: "collect",
    acceptance: [{ check: "csv-columns", file: "outputs/qc.csv", columns: ["sample"] }],
    acceptanceVerdict: { result: "holds", checks: [{ check: "csv-columns", verdict: "holds" }] }, createdAt: now },
  { runId: unverifiedRun.runId, taskIndex: 1, handle: "validator", terminalState: "completed", goalText: "validate",
    acceptance: [{ check: "json-valid", file: "outputs/qc.json" }],
    acceptanceVerdict: { result: "unverified",
      checks: [{ check: "json-valid", verdict: "unverified", code: "evidence_timeout", detail: "evidence evaluation exceeded its deadline" }] },
    createdAt: now },
];
const failedNodes = [
  { runId: failedRun.runId, taskIndex: 0, handle: "reporter", terminalState: "failed", goalText: "report",
    errorCode: "acceptance_failed",
    acceptance: [{ check: "non-empty", file: "outputs/report.md" }],
    acceptanceVerdict: { result: "does_not_hold",
      checks: [{ check: "non-empty", verdict: "does_not_hold", code: "file_empty", detail: "outputs/report.md is empty" }] },
    createdAt: now },
];
const plainNodes = [
  { runId: plainRun.runId, taskIndex: 0, handle: "summariser", terminalState: "completed", goalText: "summarise", createdAt: now },
];

function fulfill(route: Route, data: unknown) {
  return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockGraphTab(page: Page) {
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}/graph-runs`) {
      return fulfill(route, [unverifiedRun, failedRun, plainRun]);
    }
    if (path === `/v1/sessions/${session.id}/graph-runs/${unverifiedRun.runId}`) {
      return fulfill(route, { run: unverifiedRun, nodes: unverifiedNodes, flowVersion: 1 });
    }
    if (path === `/v1/sessions/${session.id}/graph-runs/${failedRun.runId}`) {
      return fulfill(route, { run: failedRun, nodes: failedNodes, flowVersion: 1 });
    }
    if (path === `/v1/sessions/${session.id}/graph-runs/${plainRun.runId}`) {
      return fulfill(route, { run: plainRun, nodes: plainNodes, flowVersion: 1 });
    }
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages: [], hasMore: false, activeLeafMessageId: null });
    return route.abort();
  });
}

async function openGraphTab(page: Page) {
  await mockGraphTab(page);
  await openSession(page, { projectId: project.id, sessionId: session.id });
  await page.getByTitle("Show panel").click();
  await page.getByRole("tab", { name: "Graphs" }).click();
}

const card = (page: Page, name: string) => page.getByTestId("graph-run-card").filter({ hasText: name });

test("Graph acceptance verdicts are visible and never read as success", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await openGraphTab(page);

  // The newest card is expanded. A holds verdict is neutral, and its tooltip says
  // the clause is a declared file condition rather than a review of the work.
  const held = card(page, "Checked flow").locator('[data-acceptance-verdict="holds"]');
  await expect(held).toHaveText("conditions met");
  await expect(held).toHaveAttribute("title", /not a review of whether the work is correct/);

  // unverified is the warning style, never the success style, and the title
  // carries the diagnostic of the leaf that could not be decided.
  const notVerifiedRow = card(page, "Checked flow").locator("li").filter({ hasText: "validator" });
  const notVerified = notVerifiedRow.getByTestId("graph-acceptance-badge");
  await expect(notVerified).toHaveText("not verified");
  await expect(notVerified).toHaveAttribute("data-acceptance-verdict", "unverified");
  await expect(notVerified).toHaveAttribute("title", /evidence_timeout/);
  await expect(notVerified).toHaveAttribute("title", /exceeded its deadline/);
  const heldRow = card(page, "Checked flow").locator("li").filter({ hasText: "collector" });
  await expect(heldRow.getByTestId("graph-acceptance-badge")).toHaveAttribute("data-acceptance-verdict", "holds");

  // A completed Run still carries the amber marker for the undecided clause.
  const marker = card(page, "Checked flow").getByTestId("graph-acceptance-unverified");
  await expect(marker).toHaveText("1 not verified");

  // does_not_hold already fails the Run, so its card shows no second marker.
  await expect(card(page, "Missed flow").getByTestId("graph-acceptance-unverified")).toHaveCount(0);

  // Expanding the failed Run shows the error badge plus the node's errorCode, so a
  // reader sees that the child run finished and the artifacts were wrong.
  await card(page, "Missed flow").getByRole("button").click();
  const failedRow = card(page, "Missed flow").locator("li").filter({ hasText: "reporter" });
  await expect(failedRow.getByTestId("graph-acceptance-badge")).toHaveText("conditions not met");
  await expect(failedRow.getByTestId("graph-acceptance-badge")).toHaveAttribute("data-acceptance-verdict", "does_not_hold");
  await expect(failedRow.getByText("acceptance_failed")).toBeVisible();

  // A Run without `acceptance` looks exactly as it did before the feature: no
  // badge on its tasks and no marker on its card.
  await card(page, "Plain flow").getByRole("button").click();
  await expect(card(page, "Plain flow").locator("li").filter({ hasText: "summariser" })).toBeVisible();
  await expect(page.getByTestId("graph-acceptance-badge")).toHaveCount(0);
  await expect(card(page, "Plain flow").getByTestId("graph-acceptance-unverified")).toHaveCount(0);
  // The collapsed card above it still reports its undecided clause.
  await expect(card(page, "Checked flow").getByTestId("graph-acceptance-unverified")).toBeVisible();
});
