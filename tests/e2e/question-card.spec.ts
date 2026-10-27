import { expect, test, type Page, type Route } from "@playwright/test";
import { openSession, tryFulfillBlankSessionCreate } from "./harness";
import { subscribedFrame } from "./session-feed";

const now = "2026-07-28T00:00:00Z";
const project = { id: "question-project", name: "Question project", description: "", status: "active", createdAt: now, updatedAt: now };
const session = { id: "question-session", projectId: project.id, title: "Question session", status: "active", activeLeafMessageId: "m2", createdAt: now, updatedAt: now };
const run = { id: "question-run", turnId: "turn", sessionId: session.id, runKind: "agent", attempt: 1,
  status: "waiting_for_approval", speakerSnapshot: { kind: "host", displayName: "Host" },
  requestedConfig: {}, effectiveConfig: {}, createdAt: now };
const approval = {
  id: "question-approval", runId: run.id, sessionId: session.id, iteration: 1, batchDigest: "digest",
  kind: "question", status: "pending", items: [], requestedAt: now,
  questions: [{
    id: "scope", prompt: "Which samples should I include?", allowOther: true, options: [
      { id: "All", label: "All" }, { id: "Tumor only", label: "Tumor only" },
    ],
  }],
};

function message(id: string, parentMessageId: string | undefined, role: "user" | "assistant", text: string) {
  return { id, sessionId: session.id, parentMessageId, role, status: "complete",
    speakerKind: role === "user" ? "user" : "host",
    speakerSnapshot: role === "user" ? { kind: "user", displayName: "You" } : { kind: "host", displayName: "Host" },
    visibility: "public", parts: [{ type: "text", text }], createdAt: now };
}

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mock(page: Page): Promise<{ answers: () => unknown[]; clearApproval: () => void }> {
  const bodies: unknown[] = [];
  let currentApproval: unknown = approval;
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillBlankSessionCreate(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/provider-profiles") return fulfill(route, []);
    if (path === "/v1/model-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, { run, pendingApproval: currentApproval });
    if (path === `/v1/sessions/${session.id}/context-usage`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/compactions`) return fulfill(route, []);
    if (path === `/v1/sessions/${session.id}/messages`) {
      const asked = {
        ...message("m2", "m1", "assistant", "waiting"),
        parts: [
          { type: "text", text: "waiting" },
          { type: "tool_call", toolCall: { id: "ask-call", name: "ask_user", arguments: { questions: approval.questions } } },
        ],
      };
      return fulfill(route, { messages: [message("m1", undefined, "user", "ask"), asked], hasMore: false, activeLeafMessageId: "m2" });
    }
    if (path === `/v1/sessions/${session.id}/events`) {
      return route.fulfill({ status: 200, contentType: "text/event-stream",
        body: subscribedFrame({ activeRun: run, pendingApproval: currentApproval, queuedInputs: [], checkpoints: [], delegationActive: false }) });
    }
    if (path === `/v1/approval-requests/${approval.id}/answer`) {
      bodies.push(JSON.parse(route.request().postData() ?? "{}"));
      return fulfill(route, { ...approval, status: "approved", answers: [{ questionId: "scope", values: ["Tumor only"] }] });
    }
    return route.abort();
  });
  return { answers: () => bodies, clearApproval: () => { currentApproval = null; } };
}

test("renders a structured question and submits the selected option", async ({ page }) => {
  const captured = await mock(page);
  await openSession(page, { projectId: project.id, sessionId: session.id });

  await expect(page.getByText("Which samples should I include?", { exact: true })).toBeVisible();
  await expect(page.getByText("Sensitive tool call", { exact: false })).toHaveCount(0);
  await expect(page.locator('[data-tool-call-id="ask-call"]')).toContainText("Ask user");
  await expect(page.locator('[data-tool-call-id="ask-call"]')).toHaveAttribute("data-risk", "read_only");

  // Enter must not steer (and must not surface an English 409) while a question
  // is pending; the draft stays in the composer.
  const composer = page.getByRole("textbox", { name: "Steer the agent" });
  await composer.fill("use only the treated arm");
  await composer.press("Enter");
  await expect(composer).toHaveValue("use only the treated arm");
  await expect(page.getByText(/409|not active|Failed to steer/i)).toHaveCount(0);
  await expect(composer).toHaveAttribute("placeholder", "Answer the question above before sending another message");

  await page.getByRole("radio", { name: /Tumor only/ }).check();
  await page.getByRole("button", { name: "Submit answer" }).click();

  await expect.poll(() => captured.answers().length).toBe(1);
  expect(captured.answers()[0]).toMatchObject({
    answers: [{ questionId: "scope", values: ["Tumor only"] }],
  });

  // The POST response does not necessarily clear the snapshot immediately; the
  // card must stay visible as a read-only receipt of the submitted answer.
  const card = page.getByRole("region", { name: "A question for you" });
  await expect(card).toBeVisible();
  await expect(card).toContainText("Answer submitted.");
  await expect(card).toContainText("Tumor only");
  await expect(page.getByRole("button", { name: "Submit answer" })).toHaveCount(0);

  // Once the snapshot clears the approval, the card goes away.
  captured.clearApproval();
  await openSession(page, { projectId: project.id, sessionId: session.id });
  await expect(page.getByRole("region", { name: "A question for you" })).toHaveCount(0);
});

test("requires Other text when Other is selected", async ({ page }) => {
  await mock(page);
  await openSession(page, { projectId: project.id, sessionId: session.id });
  await expect(page.getByRole("button", { name: "Submit answer" })).toBeDisabled();
  await page.getByRole("radio", { name: "Other" }).check();
  await expect(page.getByRole("button", { name: "Submit answer" })).toBeDisabled();
  await page.getByPlaceholder("Type your answer").fill("Only the treated arm");
  await expect(page.getByRole("button", { name: "Submit answer" })).toBeEnabled();
});
