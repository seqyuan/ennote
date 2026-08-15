"use client";

import { MoreHorizontal, Archive, RotateCcw } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { Session } from "@/components/settings/types";
import type { SessionLifecycleView } from "@/hooks/useProjectSessions";

export function SessionActionMenu({
  session,
  view,
  archiveSession,
  restoreSession,
  mutatingId,
}: {
  session: Session;
  view: SessionLifecycleView;
  archiveSession: (s: Session) => void;
  restoreSession: (s: Session) => void;
  mutatingId: string | null;
}) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const close = (e: PointerEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [open]);

  return (
    <div className="session-actions" ref={rootRef}>
      <button
        type="button"
        className="session-action-trigger"
        aria-label={`Actions for ${session.title}`}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((c) => !c)}
      >
        <MoreHorizontal size={15} aria-hidden="true" />
      </button>
      {open && (
        <div className="session-action-menu" role="menu">
          <button
            type="button"
            role="menuitem"
            disabled={mutatingId === session.id}
            onClick={() => {
              setOpen(false);
              if (view === "active") archiveSession(session);
              else restoreSession(session);
            }}
          >
            {view === "active" ? <Archive size={14} aria-hidden="true" /> : <RotateCcw size={14} aria-hidden="true" />}
            {view === "active" ? "Archive session" : "Restore session"}
          </button>
        </div>
      )}
    </div>
  );
}
