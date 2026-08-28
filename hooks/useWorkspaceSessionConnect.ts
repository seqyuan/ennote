"use client";

import { useEffect, useRef } from "react";
import type { Session } from "@/components/settings/types";
import {
  readStoredId, SELECTED_SESSION_KEY, writeStoredId,
} from "@/lib/ensure-blank-session";
import { isBlankSession } from "@/lib/session-blank";

/**
 * dsh watchNavigation: on the first catalog+session-list ready tick, restore
 * the stored current session, otherwise connectWorkspace (reuse-or-create
 * blank). After that, only reuse an existing blank when the user lands on a
 * project with no selection — never spawn a second blank behind their back.
 */
export function useWorkspaceSessionConnect(opts: {
  projectsReady: boolean;
  selectedProject: string | null;
  selectedSession: string | null;
  sessions: Session[];
  sessionsLoading: boolean;
  selectSession: (sessionId: string | null) => void;
  connectBlank: (projectId: string) => Promise<void>;
}): void {
  const {
    projectsReady, selectedProject, selectedSession, sessions, sessionsLoading,
    selectSession, connectBlank,
  } = opts;
  const phase = useRef<"waiting" | "connecting" | "done">("waiting");

  useEffect(() => {
    if (!projectsReady) return;
    if (!selectedProject) {
      if (selectedSession) selectSession(null);
      phase.current = "done";
      return;
    }
    if (sessionsLoading) return;

    if (selectedSession && sessions.some((session) => session.id === selectedSession)) {
      writeStoredId(SELECTED_SESSION_KEY, selectedSession);
      phase.current = "done";
      return;
    }

    if (phase.current === "done") {
      if (selectedSession) return;
      const blank = sessions.find(isBlankSession);
      if (blank) selectSession(blank.id);
      return;
    }

    if (phase.current === "connecting") return;

    const stored = readStoredId(SELECTED_SESSION_KEY);
    if (stored && sessions.some((session) => session.id === stored)) {
      selectSession(stored);
      phase.current = "done";
      return;
    }

    phase.current = "connecting";
    if (selectedSession) selectSession(null);
    void connectBlank(selectedProject).finally(() => {
      phase.current = "done";
    });
  }, [
    projectsReady, selectedProject, selectedSession, sessions, sessionsLoading,
    selectSession, connectBlank,
  ]);
}
