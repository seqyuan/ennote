"use client";

import { Archive, MoreHorizontal, Pencil, RotateCcw } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { Session } from "@/components/settings/types";
import type { SessionLifecycleView } from "@/hooks/useProjectSessions";
import { useT } from "@/components/LocaleProvider";
import { SidebarRenameDialog } from "./SidebarRenameDialog";

/** Build a simple session tree from sourceSessionId parent references */
export interface SessionTreeNode {
  session: Session;
  children: SessionTreeNode[];
}

export function buildSessionTree(sessions: Session[]): SessionTreeNode[] {
  const byId = new Map<string, SessionTreeNode>(
    sessions.map((session) => [session.id, { session, children: [] as SessionTreeNode[] }]),
  );
  const roots: SessionTreeNode[] = [];

  for (const node of byId.values()) {
    const parentId = node.session.sourceSessionId;
    if (!parentId || !byId.has(parentId) || wouldCreateCycle(node.session.id, parentId, byId)) {
      roots.push(node);
      continue;
    }
    byId.get(parentId)!.children.push(node);
  }

  const sortNodes = (nodes: SessionTreeNode[]) => {
    nodes.sort((left, right) => right.session.updatedAt.localeCompare(left.session.updatedAt));
    nodes.forEach((node) => sortNodes(node.children));
  };
  sortNodes(roots);
  return roots;
}

function wouldCreateCycle(sessionId: string, parentId: string, nodes: Map<string, SessionTreeNode>): boolean {
  const visited = new Set<string>([sessionId]);
  let current: string | undefined = parentId;
  while (current) {
    if (visited.has(current)) return true;
    visited.add(current);
    current = nodes.get(current)?.session.sourceSessionId;
  }
  return false;
}

/** Compact relative time, dsh-style ("now"/"5min" in en, "刚刚"/"5分钟" in zh). */
export function formatRelativeTime(updatedAt: string, t: (key: string) => string): string {
  const MIN = 60_000;
  const HOUR = 3_600_000;
  const DAY = 86_400_000;
  const MONTH = 30 * DAY;
  const YEAR = 365 * DAY;
  const diff = Math.max(0, Date.now() - new Date(updatedAt).getTime());
  if (diff < MIN) return t("time.now");
  if (diff < HOUR) return `${Math.floor(diff / MIN)}${t("time.minutes")}`;
  if (diff < DAY) return `${Math.floor(diff / HOUR)}${t("time.hours")}`;
  if (diff < MONTH) return `${Math.floor(diff / DAY)}${t("time.days")}`;
  if (diff < YEAR) return `${Math.floor(diff / MONTH)}${t("time.months")}`;
  return `${Math.floor(diff / YEAR)}${t("time.years")}`;
}

export function renderSessionTree(
  nodes: SessionTreeNode[],
  selectedSession: string | null,
  switchSession: (id: string) => void,
  view: SessionLifecycleView,
  archiveSession: (s: Session) => void,
  restoreSession: (s: Session) => void,
  renameSession: (s: Session, title: string) => Promise<void>,
  mutatingId: string | null,
  depth: number,
  runningSessionIds?: Set<string>,
): React.ReactNode[] {
  return nodes.flatMap((node) => {
    const s = node.session;
    const isSelected = s.id === selectedSession;
    const isRunning = runningSessionIds?.has(s.id);
    return [
      <SessionRow
        key={s.id}
        session={s}
        view={view}
        isSelected={isSelected}
        isRunning={isRunning}
        depth={depth}
        mutatingId={mutatingId}
        switchSession={switchSession}
        archiveSession={archiveSession}
        restoreSession={restoreSession}
        renameSession={renameSession}
      />,
      ...renderSessionTree(node.children, selectedSession, switchSession, view, archiveSession, restoreSession, renameSession, mutatingId, depth + 1, runningSessionIds),
    ];
  });
}

function SessionRow({
  session, view, isSelected, isRunning, depth, mutatingId,
  switchSession, archiveSession, restoreSession, renameSession,
}: {
  session: Session;
  view: SessionLifecycleView;
  isSelected: boolean;
  isRunning: boolean | undefined;
  depth: number;
  mutatingId: string | null;
  switchSession: (id: string) => void;
  archiveSession: (s: Session) => void;
  restoreSession: (s: Session) => void;
  renameSession: (s: Session, title: string) => Promise<void>;
}) {
  const [menuOpen, setMenuOpen] = useState(false);
  const [renameOpen, setRenameOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const t = useT();

  useEffect(() => {
    if (!menuOpen) return;
    const close = (event: PointerEvent) => {
      if (!menuRef.current?.contains(event.target as Node)) setMenuOpen(false);
    };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [menuOpen]);

  return (
    <>
      <li className={`sb-session-row${isSelected ? " sb-selected" : ""}${menuOpen ? " sb-menu-open" : ""}`} style={{ marginLeft: depth * 22 }}>
        <div
          className="sb-session-body"
          role="button"
          tabIndex={0}
          aria-current={isSelected ? "page" : undefined}
          aria-label={session.title}
          onClick={() => view === "active" && switchSession(session.id)}
          onKeyDown={(e) => { if ((e.key === "Enter" || e.key === " ") && view === "active") { e.preventDefault(); switchSession(session.id); } }}
        >
          <span className="sb-row-slot sb-session-status">
            {isRunning && <span className="sb-running-dot" aria-hidden="true" />}
          </span>
          <span className="sb-session-title">{session.title}</span>
          <span className="sb-session-time" aria-hidden="true">{formatRelativeTime(session.updatedAt, t)}</span>
        </div>
        <span className="sb-row-actions" ref={menuRef}>
          <button
            type="button"
            className="sb-row-icon-btn"
            aria-label={`Actions for ${session.title}`}
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            onClick={(e) => { e.stopPropagation(); setMenuOpen((v) => !v); }}
          >
            <MoreHorizontal size={15} aria-hidden="true" />
          </button>
          {menuOpen && (
            <div className="sb-row-menu" role="menu">
              <button type="button" role="menuitem" disabled={mutatingId === session.id || view === "archived"} onClick={(e) => { e.stopPropagation(); setMenuOpen(false); setRenameOpen(true); }}>
                <Pencil size={13} aria-hidden="true" /> {t("sidebar.renameSession")}
              </button>
              <button
                type="button"
                role="menuitem"
                disabled={mutatingId === session.id}
                onClick={(e) => {
                  e.stopPropagation();
                  setMenuOpen(false);
                  if (view === "active") archiveSession(session);
                  else restoreSession(session);
                }}
              >
                {view === "active" ? <Archive size={13} aria-hidden="true" /> : <RotateCcw size={13} aria-hidden="true" />}
                {view === "active" ? t("sidebar.archive") : t("sidebar.restore")}
              </button>
            </div>
          )}
        </span>
      </li>
      {renameOpen && (
        <SidebarRenameDialog
          title={t("sidebar.renameSession")}
          initialName={session.title}
          onCancel={() => setRenameOpen(false)}
          onConfirm={async (name) => { await renameSession(session, name); setRenameOpen(false); }}
        />
      )}
    </>
  );
}
