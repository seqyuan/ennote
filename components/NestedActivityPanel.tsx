"use client";

import { Check, RefreshCw, Users, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { ChildRunRow } from "@/components/ChildRunRow";
import { DelegationFollowUpDialog } from "@/components/DelegationFollowUpDialog";
import { DelegationGenerationHistory } from "@/components/DelegationGenerationHistory";
import { useChildProgress } from "@/hooks/useChildProgress";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type ActivityPage = components["schemas"]["DelegationActivityPage"];
type ActivityGroup = components["schemas"]["DelegationActivityGroup"];
type DelegationInspection = components["schemas"]["DelegationInspection"];

const pollIntervalMs = 1200;
const emptyPollLimit = 20;
const emptyProgress: ReadonlyMap<string, string> = new Map();

export function NestedActivityPanel({ parentRunId, toolCallId }: { parentRunId: string; toolCallId: string }) {
  const [page, setPage] = useState<ActivityPage | null>(null);
  const [inspections, setInspections] = useState<Record<string, DelegationInspection>>({});
  const [error, setError] = useState<string | null>(null);
  const [emptyPolling, setEmptyPolling] = useState(true);
  const [retry, setRetry] = useState(0);
  const [open, setOpen] = useState(true);
  const [busy, setBusy] = useState<string | null>(null);
  const childProgress = useChildProgress();
  const [continuation, setContinuation] = useState<{
    itemID: string;
    itemName: string;
    kind: "input" | "follow_up";
    sourceAttemptID: string;
    expectedGeneration: number;
  } | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    let timer: number | undefined;
    let emptyPolls = 0;
    const poll = async () => {
      try {
        const next = await apiFetch<ActivityPage>(`/v1/runs/${encodeURIComponent(parentRunId)}/children`, {
          signal: controller.signal,
        });
        if (controller.signal.aborted) return;
        setPage(next);
        setError(null);
        const groups = next.groups.filter(group => group.parentToolCallId === toolCallId);
        const active = groups.some(group => groupIsActive(group));
        if (groups.length === 0) emptyPolls += 1;
        else emptyPolls = 0;
        const keepPolling = active || (groups.length === 0 && emptyPolls < emptyPollLimit);
        setEmptyPolling(groups.length === 0 && keepPolling);
        if (keepPolling) timer = window.setTimeout(poll, pollIntervalMs);
      } catch (reason) {
        if (controller.signal.aborted) return;
        setError((reason as Error).message);
        setEmptyPolling(false);
      }
    };
    void poll();
    return () => {
      controller.abort();
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [parentRunId, retry, toolCallId]);

  const groups = useMemo(
    () => page?.groups.filter(group => group.parentToolCallId === toolCallId) ?? [],
    [page, toolCallId],
  );
  const groupIDs = useMemo(() => groups.map(group => group.id).join(","), [groups]);

  // Load the inspection projection (generations, valid actions, pending
  // authorization) for each visible group.
  useEffect(() => {
    const controller = new AbortController();
    for (const groupID of groupIDs.split(",").filter(Boolean)) {
      void apiFetch<DelegationInspection>(`/v1/delegations/${encodeURIComponent(groupID)}`, {
        signal: controller.signal,
      }).then(inspection => {
        if (!controller.signal.aborted) {
          setInspections(previous => ({ ...previous, [groupID]: inspection }));
        }
      }).catch(() => {
        // Inspection is a progressive enhancement; children polling is source.
      });
    }
    return () => controller.abort();
  }, [groupIDs]);

  const children = groups.flatMap(group => group.children);
  const activeCount = children.filter(child => child.runStatus && !isTerminalStatus(child.runStatus)).length;

  const retryItem = useCallback(async (group: ActivityGroup, itemId: string) => {
    if (busy) return;
    const inspection = inspections[group.id];
    const expectedGeneration = inspection?.currentGeneration ?? 0;
    setBusy(itemId);
    try {
      await apiFetch<{ generation: unknown }>(`/v1/delegations/${encodeURIComponent(group.id)}/retry`, {
        method: "POST",
        body: JSON.stringify({
          expectedGeneration,
          itemIds: [itemId],
          clientRequestId: crypto.randomUUID(),
        }),
      });
      setRetry(value => value + 1); // refresh children + inspections
    } catch (reason) {
      // Stale expected generation simply refreshes: the poll below re-reads the
      // authoritative state instead of overwriting anything locally.
      setError((reason as Error).message);
      setRetry(value => value + 1);
    } finally {
      setBusy(null);
    }
  }, [busy, inspections]);

  const decideApproval = useCallback(async (approvalID: string, decision: "approved" | "rejected") => {
    if (busy) return;
    setBusy(approvalID + decision);
    try {
      await apiFetch(`/v1/delegation-approvals/${encodeURIComponent(approvalID)}/decision`, {
        method: "POST",
        body: JSON.stringify({ decision, clientRequestId: crypto.randomUUID() }),
      });
      setRetry(value => value + 1);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(null);
    }
  }, [busy]);

  return <details className="nested-activity" open={open} onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>
      <span><Users size={14} aria-hidden="true" /> Delegated roles</span>
      <span>{children.length ? `${children.length} ${activeCount ? `· ${activeCount} active` : "· settled"}` : ""}</span>
    </summary>
    <div className="nested-activity-body">
      {!page && !error && <div className="nested-activity-state">Loading delegated activity…</div>}
      {page && groups.length === 0 && emptyPolling && <div className="nested-activity-state">Preparing delegated runs…</div>}
      {page && groups.length === 0 && !emptyPolling && !error &&
        <div className="nested-activity-state">No delegated runs were recorded.</div>}
      {error && <div className="nested-activity-error" role="status">
        <span>Delegation activity unavailable</span>
        <button type="button" aria-label="Retry delegation activity" title="Retry" onClick={() => setRetry(value => value + 1)}>
          <RefreshCw size={14} aria-hidden="true" />
        </button>
      </div>}
      {groups.map(group => <GroupBlock
        key={group.id}
        group={group}
        inspection={inspections[group.id]}
        busy={busy}
        progress={childProgress.get(group.id) ?? emptyProgress}
        onRetry={retryItem}
        onDecide={decideApproval}
        onContinue={(itemID, itemName, kind, sourceAttemptID, expectedGeneration) =>
          setContinuation({ itemID, itemName, kind, sourceAttemptID, expectedGeneration })}
      />)}
    </div>
    {continuation && <DelegationFollowUpDialog
      itemID={continuation.itemID} itemName={continuation.itemName} kind={continuation.kind}
      sourceAttemptID={continuation.sourceAttemptID} expectedGeneration={continuation.expectedGeneration}
      onDone={() => { setContinuation(null); setRetry(value => value + 1); }} />}
  </details>;
}

function GroupBlock({ group, inspection, busy, onRetry, onDecide, onContinue, progress }: {
  group: ActivityGroup;
  inspection?: DelegationInspection;
  busy: string | null;
  onRetry: (group: ActivityGroup, itemId: string) => void;
  onDecide: (approvalID: string, decision: "approved" | "rejected") => void;
  onContinue: (itemID: string, itemName: string, kind: "input" | "follow_up", sourceAttemptID: string, expectedGeneration: number) => void;
  progress: ReadonlyMap<string, string>;
}) {
  const reusedChildRunIds = useMemo(() => {
    const ids = new Set<string>();
    for (const generation of inspection?.generations ?? []) {
      for (const reference of generation.reusedAttempts ?? []) {
        if (reference.childRunId) ids.add(reference.childRunId);
      }
    }
    return ids;
  }, [inspection]);
  const pendingApproval = inspection?.pendingApproval;
  return <>
    <div className="child-run-list" role="list">
      {group.children.map(child => <ChildRunRow
        child={child}
        key={child.itemId}
        reused={reusedChildRunIds.has(child.childRunId ?? "")}
        activity={progress.get(child.name)}
        onRetry={itemId => onRetry(group, itemId)}
        onContinue={selectedAttemptID(inspection, child.itemId) ? (itemId, kind) => {
          const sourceAttemptID = selectedAttemptID(inspection, itemId);
          onContinue(itemId, child.name, kind, sourceAttemptID, inspection?.currentGeneration ?? 0);
        } : undefined}
      />)}
    </div>
    {pendingApproval && <div className="delegation-approval-banner" role="status">
      <span>Retry budget increase awaits approval</span>
      <span className="banner-actions">
        <button type="button" className="child-run-approve" aria-label="Approve retry budget" title="Approve"
          disabled={busy !== null} onClick={() => onDecide(pendingApproval.id, "approved")}>
          <Check size={13} aria-hidden="true" /> Approve
        </button>
        <button type="button" className="child-run-reject" aria-label="Reject retry budget" title="Reject"
          disabled={busy !== null} onClick={() => onDecide(pendingApproval.id, "rejected")}>
          <X size={13} aria-hidden="true" /> Reject
        </button>
      </span>
    </div>}
    {inspection && <DelegationGenerationHistory inspection={inspection} />}
  </>;
}

function selectedAttemptID(inspection: DelegationInspection | undefined, itemID: string): string {
  if (!inspection) return "";
  const generation = inspection.generations.find(entry => entry.generation === inspection.currentGeneration);
  const reused = generation?.reusedAttempts.find(reference => reference.itemId === itemID);
  if (reused) return reused.attemptId;
  const item = inspection.items.find(entry => entry.itemId === itemID);
  return item?.attempts.find(attempt => attempt.generation === inspection.currentGeneration)?.attemptId ?? "";
}

function groupIsActive(group: ActivityGroup): boolean {
  if (group.status !== "settled" && group.status !== "cancelled") return true;
  return group.children.some(child => child.runStatus ? !isTerminalStatus(child.runStatus) : child.itemStatus === "pending" || child.itemStatus === "running");
}

function isTerminalStatus(status: NonNullable<components["schemas"]["DelegationChildActivity"]["runStatus"]>): boolean {
  return status === "succeeded" || status === "failed" || status === "cancelled" || status === "interrupted";
}
