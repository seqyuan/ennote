"use client";

import type { Session } from "@/components/settings/types";
import type { SessionLifecycleView } from "@/hooks/useProjectSessions";
import { SessionActionMenu } from "./SessionActionMenu";

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

export function renderSessionTree(
  nodes: SessionTreeNode[],
  selectedSession: string | null,
  switchSession: (id: string) => void,
  view: SessionLifecycleView,
  archiveSession: (s: Session) => void,
  restoreSession: (s: Session) => void,
  mutatingId: string | null,
  depth: number,
  runningSessionIds?: Set<string>,
): React.ReactNode[] {
  return nodes.flatMap((node) => {
    const s = node.session;
    const isSelected = s.id === selectedSession;
    const isRunning = runningSessionIds?.has(s.id);
    const SESSION_TRUNCATE_LENGTH = 80;

    return [
      <li className="session-row" key={s.id} style={{ paddingLeft: depth * 14 }}>
        <button
          type="button"
          className={`sidebar-item ${isSelected ? "active" : ""}`}
          aria-current={isSelected ? "page" : undefined}
          onClick={() => view === "active" && switchSession(s.id)}
          disabled={view === "archived"}
          style={{
            display: "flex",
            alignItems: "center",
            gap: 6,
            minWidth: 0,
            paddingRight: 4,
          }}
          title={s.title}
        >
          {isRunning && (
            <span
              style={{
                display: "inline-block",
                width: 7,
                height: 7,
                borderRadius: "50%",
                background: "#22c55e",
                flexShrink: 0,
                animation: depth === 0 ? "pulse 2s infinite" : undefined,
              }}
            />
          )}
          <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
            {s.title.length > SESSION_TRUNCATE_LENGTH
              ? s.title.slice(0, SESSION_TRUNCATE_LENGTH) + "..."
              : s.title}
          </span>
        </button>
        <SessionActionMenu
          session={s}
          view={view}
          archiveSession={archiveSession}
          restoreSession={restoreSession}
          mutatingId={mutatingId}
        />
      </li>,
      ...renderSessionTree(node.children, selectedSession, switchSession, view, archiveSession, restoreSession, mutatingId, depth + 1, runningSessionIds),
    ];
  });
}
