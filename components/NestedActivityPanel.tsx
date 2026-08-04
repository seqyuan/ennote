"use client";

import { RefreshCw, Users } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { ChildRunRow } from "@/components/ChildRunRow";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type ActivityPage = components["schemas"]["DelegationActivityPage"];
type ActivityGroup = components["schemas"]["DelegationActivityGroup"];

const pollIntervalMs = 1200;
const emptyPollLimit = 20;

export function NestedActivityPanel({ parentRunId, toolCallId }: { parentRunId: string; toolCallId: string }) {
  const [page, setPage] = useState<ActivityPage | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [emptyPolling, setEmptyPolling] = useState(true);
  const [retry, setRetry] = useState(0);
  const [open, setOpen] = useState(true);

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
  const children = groups.flatMap(group => group.children);
  const activeCount = children.filter(child => child.runStatus && !isTerminalStatus(child.runStatus)).length;

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
      {groups.map(group => <div className="child-run-list" role="list" key={group.id}>
        {group.children.map(child => <ChildRunRow child={child} key={child.itemId} />)}
      </div>)}
    </div>
  </details>;
}

function groupIsActive(group: ActivityGroup): boolean {
  if (group.status !== "settled" && group.status !== "cancelled") return true;
  return group.children.some(child => child.runStatus ? !isTerminalStatus(child.runStatus) : child.itemStatus === "pending" || child.itemStatus === "running");
}

function isTerminalStatus(status: NonNullable<components["schemas"]["DelegationChildActivity"]["runStatus"]>): boolean {
  return status === "succeeded" || status === "failed" || status === "cancelled" || status === "interrupted";
}
