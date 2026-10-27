"use client";

import { CircleAlert, CircleCheck, LoaderCircle, PauseCircle, RotateCw } from "lucide-react";
import { useT } from "@/components/LocaleProvider";
import { runStatusMessageKey } from "@/lib/run-status-copy";

export interface RunStatusProps {
  status: string;
  active: boolean;
  waiting: boolean;
  waitingForQuestion?: boolean;
  reconnecting: boolean;
  compacting: boolean;
  permissionMode?: string;
}

export function RunStatus({ status, active, waiting, waitingForQuestion, reconnecting, compacting, permissionMode }: RunStatusProps) {
  const t = useT();
  if (!active && !status) return null;
  const statusKey = runStatusMessageKey(status);
  const label = reconnecting ? "Reconnecting to run" : waiting
    ? t(waitingForQuestion ? "run.status.waitingAnswer" : "run.status.waitingApproval")
    : statusKey ? t(statusKey) : status || (compacting ? "Compacting context" : "Running");
  const Icon = reconnecting ? RotateCw : waiting ? PauseCircle : active ? LoaderCircle : status.toLowerCase().includes("fail") ? CircleAlert : CircleCheck;
  return <div className={`run-status ${active ? "is-active" : ""} ${waiting ? "is-waiting" : ""}`} role="status">
    <Icon size={14} aria-hidden="true" className={active && !waiting ? "status-spin" : ""} />
    <span>{label}</span>
    {active && permissionMode && <span className="run-mode">{permissionMode} · frozen</span>}
  </div>;
}
