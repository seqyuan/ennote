"use client";

import { Check, GitBranch } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { components } from "@/lib/worker-api.gen";

type SessionBranch = components["schemas"]["SessionBranch"];

interface BranchControlProps {
  branches: SessionBranch[];
  activeBranchId?: string;
  loading: boolean;
  changing: boolean;
  disabled: boolean;
  activate: (branchId: string) => void;
}

export function BranchControl({ branches, activeBranchId, loading, changing, disabled, activate }: BranchControlProps) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const active = branches.find(branch => branch.id === activeBranchId);

  useEffect(() => {
    if (!open) return;
    const close = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    window.addEventListener("pointerdown", close);
    return () => window.removeEventListener("pointerdown", close);
  }, [open]);

  return <div className="branch-control" ref={root}>
    <button type="button" className="branch-trigger" aria-label="Choose conversation branch" title="Choose conversation branch"
      aria-haspopup="menu" aria-expanded={open} disabled={disabled || loading || changing}
      onClick={() => setOpen(value => !value)}>
      <GitBranch size={15} aria-hidden="true" />
      <span>{active?.label ?? (loading ? "Loading" : "Main")}</span>
      {branches.length > 1 && <small>{branches.length}</small>}
    </button>
    {open && <div className="branch-menu" role="menu" aria-label="Conversation branches">
      {branches.map(branch => <button type="button" role="menuitemradio" aria-checked={branch.id === activeBranchId}
        key={branch.id} onClick={() => { setOpen(false); activate(branch.id); }}>
        <GitBranch size={14} aria-hidden="true" />
        <span><strong>{branch.label}</strong><small>{branch.messageCount} messages</small></span>
        {branch.id === activeBranchId && <Check size={14} aria-hidden="true" />}
      </button>)}
    </div>}
  </div>;
}
