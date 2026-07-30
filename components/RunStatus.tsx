"use client";

import { CircleAlert, CircleCheck, LoaderCircle, PauseCircle, RotateCw } from "lucide-react";

export interface RunStatusProps {
  status: string;
  active: boolean;
  waiting: boolean;
  reconnecting: boolean;
  compacting: boolean;
  permissionMode?: string;
}

export function RunStatus({ status, active, waiting, reconnecting, compacting, permissionMode }: RunStatusProps) {
  if (!active && !status) return null;
  const label = reconnecting ? "Reconnecting to run" : waiting ? "Waiting for approval" : status || (compacting ? "Compacting context" : "Running");
  const Icon = reconnecting ? RotateCw : waiting ? PauseCircle : active ? LoaderCircle : status.toLowerCase().includes("fail") ? CircleAlert : CircleCheck;
  return <div className={`run-status ${active ? "is-active" : ""} ${waiting ? "is-waiting" : ""}`} role="status">
    <Icon size={14} aria-hidden="true" className={active && !waiting ? "status-spin" : ""} />
    <span>{label}</span>
    {active && permissionMode && <span className="run-mode">{permissionMode} · frozen</span>}
  </div>;
}
