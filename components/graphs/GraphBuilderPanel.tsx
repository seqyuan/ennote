"use client";

import { AlertTriangle, Bot, Check, Loader2, Send } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import type { GraphDetail, ModelOption } from "@/components/graphs/types";
import { apiFetch } from "@/lib/worker-api.client";

interface BuilderMessage { id: string; role: "user" | "assistant"; content: string; ordinal: number }
interface BuilderOperation { kind: string; taskId?: string; depends?: string[] }
interface BuilderProposal { id: string; summary: string; operations: BuilderOperation[]; diagnostics: string[]; status: string }
interface BuilderThread { graphId: string; modelProfileId?: string; messages: BuilderMessage[]; proposal?: BuilderProposal }

export function GraphBuilderPanel({ graphId, models, onApplied, setError }: {
  graphId: string;
  models: ModelOption[];
  onApplied: (detail: GraphDetail) => void;
  setError: (message: string | null) => void;
}) {
  const [thread, setThread] = useState<BuilderThread | null>(null);
  const [modelId, setModelId] = useState("");
  const [instruction, setInstruction] = useState("");
  const [busy, setBusy] = useState<"loading" | "sending" | "applying" | null>(null);

  const load = useCallback(async () => {
    setBusy("loading");
    try {
      const next = await apiFetch<BuilderThread>(`/v1/graphs/${encodeURIComponent(graphId)}/builder`);
      setThread(next);
      setModelId(next.modelProfileId || models[0]?.model.id || "");
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  }, [graphId, models, setError]);

  useEffect(() => {
    const timer = window.setTimeout(() => void load(), 0);
    return () => window.clearTimeout(timer);
  }, [load]);

  const send = async () => {
    if (!instruction.trim() || !modelId || busy) return;
    setBusy("sending");
    try {
      const next = await apiFetch<BuilderThread>(`/v1/graphs/${encodeURIComponent(graphId)}/builder/messages`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ modelProfileId: modelId, instruction: instruction.trim() }),
      });
      setThread(next);
      setInstruction("");
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const apply = async () => {
    if (!thread?.proposal || thread.proposal.diagnostics.length > 0 || busy) return;
    setBusy("applying");
    try {
      const detail = await apiFetch<GraphDetail>(`/v1/graphs/${encodeURIComponent(graphId)}/builder/proposals/${encodeURIComponent(thread.proposal.id)}/apply`, { method: "POST" });
      onApplied(detail);
      await load();
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  };

  return (
    <aside className="graph-builder-pane">
      <div className="graph-section-heading"><div><span>Builder</span><p>Persistent conversation for this Graph.</p></div><Bot size={18} /></div>
      <div className="graph-builder-content">
        <label className="graph-builder-model">Builder model
          <select value={modelId} onChange={(event) => setModelId(event.target.value)} disabled={Boolean(busy)}>
            {models.map((option) => <option key={option.model.id} value={option.model.id}>{option.label}</option>)}
          </select>
        </label>
        <div className="graph-builder-messages" aria-live="polite">
          {busy === "loading" && <div className="graph-builder-loading"><Loader2 className="spin" size={16} /> Loading conversation…</div>}
          {thread?.messages.map((message) => <div key={message.id} className={`graph-builder-message is-${message.role}`}><span>{message.role === "user" ? "You" : "Builder"}</span><p>{message.content}</p></div>)}
          {!busy && (thread?.messages.length ?? 0) === 0 && <div className="graph-builder-intro"><Bot size={22} /><p>Describe Tasks, execution choices, and dependencies. Builder will return a validated proposal.</p></div>}
        </div>
        {thread?.proposal && <div className={`graph-builder-proposal ${thread.proposal.diagnostics.length ? "is-invalid" : ""}`}>
          <header>{thread.proposal.diagnostics.length ? <AlertTriangle size={15} /> : <Check size={15} />}<strong>Proposal</strong><span>{thread.proposal.operations.length} changes</span></header>
          <p>{thread.proposal.summary}</p>
          <ul>{thread.proposal.operations.map((operation, index) => <li key={index}><code>{operation.kind}</code>{operation.taskId ? ` · ${operation.taskId}` : ""}</li>)}</ul>
          {thread.proposal.diagnostics.map((diagnostic) => <div className="graph-builder-diagnostic" key={diagnostic}>{diagnostic}</div>)}
          <button type="button" onClick={() => void apply()} disabled={thread.proposal.diagnostics.length > 0 || Boolean(busy)}>{busy === "applying" ? "Applying…" : "Apply proposal"}</button>
        </div>}
        <div className="graph-builder-composer">
          <textarea value={instruction} onChange={(event) => setInstruction(event.target.value)} aria-label="Graph Builder instruction" placeholder="Describe the Graph change…" disabled={Boolean(busy) || !modelId} onKeyDown={(event) => { if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) void send(); }} />
          <button type="button" onClick={() => void send()} disabled={!instruction.trim() || !modelId || Boolean(busy)} aria-label="Send Builder instruction">{busy === "sending" ? <Loader2 className="spin" size={17} /> : <Send size={17} />}</button>
        </div>
      </div>
    </aside>
  );
}
