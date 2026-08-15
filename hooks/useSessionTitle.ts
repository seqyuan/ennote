import { useCallback, useRef, useState } from "react";
import type { KeyboardEvent, RefObject } from "react";
import type { Session } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";

/**
 * Session title editing: display title derivation, inline rename state and
 * the PATCH-through persistence. `replaceSession` comes from the caller so the
 * updated Session record lands back in the project-sessions store.
 */
export function useSessionTitle(deps: {
  session: Session | null | undefined;
  replaceSession: (s: Session) => void;
}): {
  title: string;
  editing: boolean;
  draft: string;
  setDraft: (v: string) => void;
  startEdit: () => void;
  save: () => void;
  keyDown: (e: KeyboardEvent<HTMLInputElement>) => void;
  inputRef: RefObject<HTMLInputElement | null>;
} {
  const { session, replaceSession } = deps;
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  const getTitle = useCallback((s: typeof session) => {
    if (!s) return "";
    return s.title || s.id?.slice(0, 12) || "";
  }, []);

  const startEdit = useCallback(() => {
    if (!session) return;
    setDraft(session.title || getTitle(session));
    setEditing(true);
    setTimeout(() => inputRef.current?.select(), 0);
  }, [getTitle, session]);

  const save = useCallback(async () => {
    if (!session) { setEditing(false); return; }
    const name = draft.trim();
    setEditing(false);
    if (name === (session.title ?? "")) return;
    try {
      const res = await apiFetch<Session>(`/v1/sessions/${encodeURIComponent(session.id)}`, {
        method: "PATCH",
        body: JSON.stringify({ title: name }),
      });
      replaceSession(res);
    } catch { /* keep local title unchanged */ }
  }, [session, draft, replaceSession]);

  const keyDown = useCallback((event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Enter") { event.preventDefault(); void save(); }
    else if (event.key === "Escape") setEditing(false);
  }, [save]);

  return {
    title: session ? getTitle(session) : "No session",
    editing,
    draft,
    setDraft,
    startEdit,
    save,
    keyDown,
    inputRef,
  };
}
