"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import type { ActiveRunState, AgentRun, ApprovalDecision, ToolApprovalRequest } from "@/lib/approval";
import type { TurnMessage } from "@/lib/chat-messages";
import { runFailureMessage } from "@/lib/provider-errors";
import { apiFetch } from "@/lib/worker-api.client";

interface UseAgentSessionInput {
  sessionId: string | null;
  lineageId?: string;
  appendMessage: (message: TurnMessage) => void;
  upsertMessage: (message: TurnMessage) => void;
  refreshLatest: () => Promise<void>;
  refreshSession: () => Promise<unknown>;
}

function genId(): string {
  if (typeof crypto !== "undefined" && crypto.randomUUID) return crypto.randomUUID();
  return Math.random().toString(36).slice(2) + Date.now().toString(36);
}

export function useAgentSession({ sessionId, lineageId, appendMessage, upsertMessage, refreshLatest, refreshSession }: UseAgentSessionInput) {
  const [activeRun, setActiveRun] = useState<AgentRun | null>(null);
  const [pendingApproval, setPendingApproval] = useState<ToolApprovalRequest | null>(null);
  const [resolvingApproval, setResolvingApproval] = useState<ApprovalDecision | null>(null);
  const [status, setStatus] = useState("");
  const [error, setError] = useState<string | null>(null);
  const streamController = useRef<AbortController | null>(null);
  const decisionController = useRef<AbortController | null>(null);
  const generation = useRef(0);
  const decisionGeneration = useRef(0);
  const currentSession = useRef(sessionId);
  const streamConnected = useRef(false);

  useEffect(() => { currentSession.current = sessionId; }, [sessionId]);

  const refreshPendingApproval = useCallback(async (expectedRunID: string, version: number) => {
    const selected = currentSession.current;
    const expectedDecisionVersion = decisionGeneration.current;
    if (!selected) return;
    try {
      const active = await apiFetch<ActiveRunState | null>(`/v1/sessions/${encodeURIComponent(selected)}/active-run`);
      if (generation.current !== version || decisionGeneration.current !== expectedDecisionVersion ||
        active?.run.id !== expectedRunID) return;
      setActiveRun(active.run);
      setPendingApproval(active.pendingApproval ?? null);
    } catch (err) {
      if (generation.current === version) setError((err as Error).message);
    }
  }, []);

  const watchRun = useCallback(async (run: AgentRun, restoredApproval?: ToolApprovalRequest | null) => {
    const selected = currentSession.current;
    if (!selected || run.sessionId !== selected) return;
    const version = ++generation.current;
    streamController.current?.abort();
    const controller = new AbortController();
    streamController.current = controller;
    setActiveRun(run);
    setPendingApproval(restoredApproval ?? null);
    setStatus(run.runKind === "context_compaction" ? "Compaction queued…" : "Running…");
    let terminal = false;
    streamConnected.current = true;
    try {
      if (run.runKind === "context_compaction") {
        terminal = await streamCompactionEvents(run.id, setStatus, controller.signal);
      } else {
        terminal = await streamAgentEvents(run.id, upsertMessage, setStatus, controller.signal, {
          requested: () => void refreshPendingApproval(run.id, version),
          resolved: () => { if (generation.current === version) setPendingApproval(null); },
        });
      }
    } catch (err) {
      terminal = err instanceof TerminalRunError;
      if (!controller.signal.aborted && generation.current === version) setError((err as Error).message);
    } finally {
      if (generation.current === version) streamConnected.current = false;
      if (!terminal && !controller.signal.aborted && generation.current === version) {
        setStatus("Run connection interrupted");
      }
      if (terminal && !controller.signal.aborted && generation.current === version && currentSession.current === selected) {
        setActiveRun(null);
        setPendingApproval(null);
        setResolvingApproval(null);
        streamController.current = null;
        await refreshSession().catch(() => null);
        await refreshLatest();
        window.setTimeout(() => {
          if (generation.current === version && currentSession.current === selected) setStatus("");
        }, 1600);
      }
    }
  }, [refreshLatest, refreshPendingApproval, refreshSession, upsertMessage]);

  useEffect(() => {
    const version = ++generation.current;
    streamController.current?.abort();
    decisionController.current?.abort();
    queueMicrotask(() => {
      if (generation.current !== version) return;
      setActiveRun(null);
      setPendingApproval(null);
      setResolvingApproval(null);
      setStatus("");
      setError(null);
    });
    if (!sessionId) return;
    const controller = new AbortController();
    apiFetch<ActiveRunState | null>(`/v1/sessions/${encodeURIComponent(sessionId)}/active-run`, { signal: controller.signal })
      .then(active => {
        if (!active || controller.signal.aborted || generation.current !== version || currentSession.current !== sessionId) return;
        void watchRun(active.run, active.pendingApproval ?? null);
      })
      .catch(err => {
        if (!controller.signal.aborted && generation.current === version) setError((err as Error).message);
      });
    return () => controller.abort();
  }, [lineageId, sessionId, watchRun]);

  useEffect(() => {
    if (status !== "Run connection interrupted" || !activeRun || pendingApproval || !sessionId) return;
    const selected = sessionId;
    const runID = activeRun.id;
    const version = generation.current;
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      apiFetch<ActiveRunState | null>(`/v1/sessions/${encodeURIComponent(selected)}/active-run`, { signal: controller.signal })
        .then(active => {
          if (controller.signal.aborted || generation.current !== version || currentSession.current !== selected) return;
          if (!active || active.run.id !== runID) {
            setActiveRun(null);
            setStatus("");
            return Promise.all([refreshSession().catch(() => null), refreshLatest()]);
          }
          if (active.pendingApproval) {
            setActiveRun(active.run);
            setPendingApproval(active.pendingApproval);
            setStatus("Waiting for approval");
            return;
          }
          void watchRun(active.run);
        })
        .catch(err => {
          if (!controller.signal.aborted && generation.current === version) setError((err as Error).message);
        });
    }, 1000);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [activeRun, pendingApproval, refreshLatest, refreshSession, sessionId, status, watchRun]);

  const cancel = useCallback(async () => {
    if (!activeRun) return;
    try {
      await apiFetch(`/v1/runs/${encodeURIComponent(activeRun.id)}/cancel`, { method: "POST" });
      streamController.current?.abort();
      generation.current += 1;
      setStatus("cancelled");
      setActiveRun(null);
      setPendingApproval(null);
      setResolvingApproval(null);
      await refreshSession().catch(() => null);
      await refreshLatest();
    } catch (err) {
      setError((err as Error).message);
    }
  }, [activeRun, refreshLatest, refreshSession]);

  const steer = useCallback(async (text: string) => {
    if (!activeRun || !text.trim()) return false;
    try {
      setStatus("steering…");
      await apiFetch(`/v1/runs/${encodeURIComponent(activeRun.id)}/inputs`, { method: "POST",
        body: JSON.stringify({ kind: "steer", text, clientRequestId: genId() }) });
      appendMessage({ id: genId(), role: "user", text: `↪ ${text}`, kind: "steer" });
      setStatus(activeRun.status === "waiting_for_approval" ? "Waiting for approval" : "");
      return true;
    } catch (err) {
      setError((err as Error).message);
      return false;
    }
  }, [activeRun, appendMessage]);

  const decideApproval = useCallback(async (decision: ApprovalDecision) => {
    const approval = pendingApproval;
    if (!approval || resolvingApproval) return;
    decisionController.current?.abort();
    const controller = new AbortController();
    decisionController.current = controller;
    const version = ++decisionGeneration.current;
    setResolvingApproval(decision);
    try {
      await apiFetch<ToolApprovalRequest>(`/v1/approval-requests/${encodeURIComponent(approval.id)}/decision`, {
        method: "POST", headers: { "Idempotency-Key": genId() },
        body: JSON.stringify({ decision, clientRequestId: genId() }), signal: controller.signal,
      });
      if (controller.signal.aborted || decisionGeneration.current !== version) return;
      setPendingApproval(null);
      setStatus(decision === "approved" ? "Approval granted. Resuming…" : "Approval rejected. Resuming…");
      if (!streamConnected.current && activeRun) {
        void watchRun({ ...activeRun, status: "queued" });
      }
    } catch (err) {
      if (!controller.signal.aborted && decisionGeneration.current === version) setError((err as Error).message);
    } finally {
      if (!controller.signal.aborted && decisionGeneration.current === version) setResolvingApproval(null);
    }
  }, [activeRun, pendingApproval, resolvingApproval, watchRun]);

  return {
    activeRun,
    activeRunID: activeRun?.id ?? null,
    compacting: activeRun?.runKind === "context_compaction",
    pendingApproval,
    resolvingApproval,
    status,
    setStatus,
    error,
    setError,
    watchRun,
    cancel,
    steer,
    decideApproval,
  };
}

class TerminalRunError extends Error {}

async function streamCompactionEvents(runId: string, setStatus: (status: string) => void, signal: AbortSignal): Promise<boolean> {
  const response = await fetch(`/api/worker/v1/runs/${encodeURIComponent(runId)}/events`, { signal });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  const reader = response.body?.getReader();
  if (!reader) return false;
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split("\n");
      buffer = lines.pop() ?? "";
      for (const rawLine of lines) {
        const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
        if (!line.startsWith("data:")) continue;
        const event = JSON.parse(line.slice(5).trimStart());
        switch (event.type) {
          case "context_compaction_started": setStatus("Compacting context…"); break;
          case "context_compaction_retry_scheduled": setStatus(`Retrying compaction in ${event.payload?.delayMs ?? 0} ms…`); break;
          case "context_compaction_retry_started": setStatus("Retrying compaction…"); break;
          case "context_compaction_completed": setStatus("Context checkpoint ready"); break;
          case "context_compaction_failed": throw new TerminalRunError("Context compaction failed.");
          case "context_compaction_cancelled": throw new TerminalRunError("Context compaction was cancelled.");
          case "run_succeeded": return true;
          case "run_failed": throw new TerminalRunError(runFailureMessage(event.payload?.errorCode));
          case "run_cancelled": throw new TerminalRunError("Context compaction was cancelled.");
        }
      }
    }
  } finally { reader.releaseLock(); }
  return false;
}

async function streamAgentEvents(
  runId: string,
  upsertMessage: (message: TurnMessage) => void,
  setStatus: (status: string) => void,
  signal: AbortSignal,
  approval: { requested: () => void; resolved: () => void },
): Promise<boolean> {
  const response = await fetch(`/api/worker/v1/runs/${encodeURIComponent(runId)}/events`, { signal });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  const reader = response.body?.getReader();
  if (!reader) return false;
  const decoder = new TextDecoder();
  let buffer = "";
  const assistants = new Map<number, { text: string; thinking: string }>();
  const toolDeltas = new Map<string, { id: string; name: string; argumentsFragment: string }>();
  // Accumulate live tool stdout/stderr per (toolCallId, stream) so deltas append
  // instead of overwriting via the upsert merge.
  const toolOutputs = new Map<string, { text: string; name: string }>();
  // Track live frames: live deltas (text_delta etc.) arrive with event: live.
  // Durable frames carry the same event types for legacy compatibility, but
  // since the worker now dual-writes, we must consume ONLY the live frames for
  // rendering deltas to avoid duplicate text. Durable delta types are ignored.
  let currentEventName: string | null = null;
  let liveFrame = false;

  function assistant(iteration: number) {
    const value = assistants.get(iteration) ?? { text: "", thinking: "" };
    assistants.set(iteration, value);
    return value;
  }
  function flushAssistant(iteration: number) {
    const value = assistant(iteration);
    upsertMessage({ id: `${runId}-assistant-${iteration}`, role: "assistant", text: value.text, thinking: value.thinking });
  }
  function upsertTool(payload: Record<string, unknown>, state: TurnMessage["toolState"], fallback: string) {
    const callID = String(payload.toolCallId ?? payload.id ?? payload.recordId ?? `event-${payload.callIndex ?? "unknown"}`);
    const args = payload.arguments && typeof payload.arguments === "object" && !Array.isArray(payload.arguments)
      ? payload.arguments as Record<string, unknown> : undefined;
    upsertMessage({
      id: `${runId}-tool-${callID}`,
      role: "tool",
      kind: "tool",
      toolCallId: callID,
      toolName: String(payload.toolName ?? payload.name ?? "tool"),
      text: String(payload.content ?? payload.reason ?? fallback),
      arguments: args,
      argumentsFragment: typeof payload.argumentsFragment === "string" ? payload.argumentsFragment : undefined,
      isError: Boolean(payload.isError) || state === "failed",
      toolState: state,
    });
  }

  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split("\n");
      buffer = lines.pop() ?? "";
      for (const rawLine of lines) {
        const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
        if (line.startsWith("event:")) {
          currentEventName = line.slice(6).trim();
          liveFrame = currentEventName === "live";
          continue;
        }
        if (line.startsWith(":")) continue; // comment / heartbeat
        if (!line.startsWith("data:")) continue;
        const event = JSON.parse(line.slice(5).trimStart());
        // liveFrame must be reset after the data line; only deltas use event: live.
        const wasLive = liveFrame;
        liveFrame = false;
        const payload = (event.payload ?? {}) as Record<string, unknown>;
        const iteration = typeof payload.iteration === "number" ? payload.iteration : 1;
        switch (event.type) {
          case "text_delta": case "thinking_delta": {
            // Consume only live-frame deltas to avoid duplicates.
            if (!wasLive) break;
            if (event.type === "text_delta") assistant(iteration).text += String(payload.text ?? "");
            else assistant(iteration).thinking += String(payload.text ?? "");
            flushAssistant(iteration);
            break;
          }
          case "model_call_retry_scheduled":
            assistants.set(iteration, { text: "", thinking: "" }); flushAssistant(iteration);
            setStatus(`Retrying model in ${payload.delayMs ?? 0} ms…`); break;
          case "context_pruned": setStatus("Preparing context…"); break;
          case "context_compaction_planned": setStatus("Compaction queued…"); break;
          case "context_compaction_started": setStatus("Compacting context…"); break;
          case "context_compaction_retry_scheduled": setStatus(`Retrying compaction in ${payload.delayMs ?? 0} ms…`); break;
          case "context_compaction_completed": setStatus("Context checkpoint ready"); break;
          case "run_context_compaction_planned": setStatus("Preparing current run context…"); break;
          case "run_context_compaction_started": setStatus("Compacting current run context…"); break;
          case "run_context_compaction_retry_scheduled": setStatus(`Retrying run compaction in ${payload.delayMs ?? 0} ms…`); break;
          case "run_context_compaction_completed": setStatus("Current run context compacted"); break;
          case "run_context_compaction_failed": setStatus("Run compaction skipped"); break;
          case "run_context_compaction_cancelled": setStatus(""); break;
          case "context_checkpoint_selected": setStatus("Using context checkpoint…"); break;
          case "output_truncated": setStatus(Number(payload.partialToolCallCount ?? 0) > 0 ? "Recovering truncated tool call…" : "Model output truncated"); break;
          case "tool_call_delta": {
            if (!wasLive) break; // legacy durable deltas ignored
            const key = `${iteration}:${payload.index ?? 0}`;
            const partial = toolDeltas.get(key) ?? { id: "", name: "tool", argumentsFragment: "" };
            if (payload.id) partial.id = String(payload.id);
            if (payload.name) partial.name = String(payload.name);
            partial.argumentsFragment += String(payload.argumentsFragment ?? "");
            toolDeltas.set(key, partial);
            upsertTool({ ...payload, toolCallId: partial.id || key, toolName: partial.name,
              argumentsFragment: partial.argumentsFragment }, "pending", "Collecting tool arguments…");
            break;
          }
          case "tool_output_delta": {
            // Live tool stdout/stderr streaming: accumulate per call so text appends.
            if (!wasLive) break;
            const callID = String(payload.toolCallId ?? `event-${payload.callIndex ?? "unknown"}`);
            const stream = String(payload.stream ?? "stdout");
            const text = String(payload.text ?? "");
            const entry = toolOutputs.get(callID) ?? { text: "", name: String(payload.toolName ?? "tool") };
            if (payload.toolName) entry.name = String(payload.toolName);
            if (stream === "stderr") entry.text += `[stderr] ${text}`;
            else entry.text += text;
            toolOutputs.set(callID, entry);
            upsertMessage({
              id: `${runId}-tool-${callID}`,
              role: "tool",
              kind: "tool",
              toolCallId: callID,
              toolName: entry.name,
              text: entry.text,
              isError: false,
              toolState: "running",
            });
            break;
          }
          case "tool_call_started":
            setStatus(`Running: ${payload.toolName ?? "tool"}…`); upsertTool(payload, "running", "Running"); break;
          case "tool_call_completed": {
            setStatus("");
            const callID = String(payload.toolCallId ?? payload.recordId ?? `event-${payload.callIndex ?? "unknown"}`);
            toolOutputs.delete(callID); // final result replaces the live preview
            upsertTool(payload, payload.isError ? "failed" : "completed", "No output");
            break;
          }
          case "tool_call_failed": {
            const callID = String(payload.toolCallId ?? payload.recordId ?? `event-${payload.callIndex ?? "unknown"}`);
            toolOutputs.delete(callID);
            upsertTool(payload, "failed", "Tool call failed.");
            break;
          }
          case "tool_policy_denied":
          case "tool_policy_terminated":
          case "tool_call_skipped":
            upsertTool(payload, String(payload.reason ?? "").includes("approval_rejected") ? "rejected" : "failed", "Tool call skipped."); break;
          case "model_route_selected": setStatus(`Using ${payload.apiModel ?? "selected model"}…`); break;
          case "vision_fallback_started": setStatus("Describing image…"); break;
          case "vision_fallback_completed": setStatus("Image description ready"); break;
          case "approval_requested": setStatus("Waiting for approval"); approval.requested(); break;
          case "approval_resolved": setStatus("Resuming…"); approval.resolved(); break;
          case "run_succeeded": setStatus("Completed"); return true;
          case "run_cancelled": setStatus("Cancelled"); return true;
          case "run_interrupted": setStatus("Interrupted"); return true;
          case "run_failed": throw new TerminalRunError(runFailureMessage(payload.errorCode as string | undefined));
        }
      }
    }
  } finally { reader.releaseLock(); }
  return false;
}
