"use client";

import { Loader2 } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import type { ModelOption } from "@/components/graphs/types";
import { parseToolList, type PortableHeroDocument } from "@/lib/local-hero";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type GraphLocalHeroDetail = components["schemas"]["GraphLocalHeroDetail"];

const saveDelayMs = 400;
// ~6s of patience for the save that creates this Hero to land. Long enough for a
// slow round trip, short enough that a genuinely missing Hero still reports.
const waitingAttempts = 20;
const waitingDelayMs = 300;

/**
 * The Graph-private Hero a `local/<id>` Task runs.
 *
 * Only the four fields that decide what the Task does are editable here — model,
 * prompt, allowed tools, authority. The rest of the document keeps the defaults it
 * was created with, and the panel says so rather than showing fields it does not
 * write: a form that displays a value it will silently drop on the next save is
 * worse than no form.
 *
 * Saves are serialized per mount and always send the document currently on screen,
 * so a keystroke made during a round trip is the next save rather than a value the
 * response overwrites.
 */
export function LocalHeroPanel({ graphId, heroId, models, onError, onSaved }: {
  graphId: string;
  heroId: string;
  models: ModelOption[];
  onError: (value: string | null) => void;
  /** Called once a save has landed: the Graph header's Hero digest is now stale. */
  onSaved?: () => void;
}) {
  const [detail, setDetail] = useState<GraphLocalHeroDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);
  const document = useRef<PortableHeroDocument | null>(null);
  const digest = useRef<string | null>(null);
  const revision = useRef(0);
  const savingRef = useRef(false);
  const timer = useRef<number | null>(null);

  /**
   * The panel is mounted per (graph, Hero) — GraphSettings keys it that way — so the
   * effect only has to fetch; it never resets state, which would render twice.
   *
   * A 404 is not an error here: Add Task shows the Task (and this panel) from the
   * queue before the save that creates the Hero has landed. The reader sees
   * "waiting" and then the document, instead of an error for a save still in
   * flight.
   */
  useEffect(() => {
    let cancelled = false;
    document.current = null;
    digest.current = null;
    revision.current = 0;
    const load = async (attempt: number): Promise<void> => {
      try {
        const loaded = await apiFetch<GraphLocalHeroDetail>(
          `/v1/graphs/${encodeURIComponent(graphId)}/heroes/${encodeURIComponent(heroId)}`);
        if (cancelled) return;
        document.current = loaded.document;
        digest.current = loaded.digest;
        setDetail(loaded);
      } catch (reason) {
        if (cancelled) return;
        const missing = (reason as { status?: number }).status === 404;
        if (missing && attempt < waitingAttempts) {
          await new Promise((resolve) => setTimeout(resolve, waitingDelayMs));
          if (!cancelled) await load(attempt + 1);
          return;
        }
        setError((reason as Error).message);
      }
    };
    void load(0);
    return () => {
      cancelled = true;
      if (timer.current !== null) window.clearTimeout(timer.current);
    };
  }, [graphId, heroId]);

  const save = useCallback(async () => {
    if (savingRef.current) return;
    savingRef.current = true;
    setSaving(true);
    try {
      for (;;) {
        const sent = revision.current;
        const sentDocument = document.current;
        if (!sentDocument || !digest.current) break;
        const updated = await apiFetch<GraphLocalHeroDetail>(
          `/v1/graphs/${encodeURIComponent(graphId)}/heroes/${encodeURIComponent(heroId)}`,
          { method: "PATCH", headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ expectedDigest: digest.current, document: sentDocument }) },
        );
        digest.current = updated.digest;
        setError(null);
        onError(null);
        onSaved?.();
        if (revision.current === sent) {
          // Nothing was typed during the round trip, so the Worker's answer is what
          // is on screen; adopting it shows any value it normalised.
          document.current = updated.document;
          setDetail(updated);
          setDirty(false);
          break;
        }
        // Otherwise keep the reader's text and save again with the newer digest.
      }
    } catch (reason) {
      setError((reason as Error).message);
      setDirty(true);
    } finally {
      savingRef.current = false;
      setSaving(false);
    }
  }, [graphId, heroId, onError, onSaved]);

  const update = (mutate: (current: PortableHeroDocument) => PortableHeroDocument) => {
    if (!document.current) return;
    document.current = mutate(document.current);
    revision.current += 1;
    setDirty(true);
    if (timer.current !== null) window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => { timer.current = null; void save(); }, saveDelayMs);
  };

  if (error) {
    return <div className="graph-local-hero" role="alert"><span>{error}</span></div>;
  }
  if (!detail || !document.current) {
    return <div className="graph-local-hero"><span><Loader2 className="spin" size={13} /> Waiting for Hero {heroId}…</span></div>;
  }

  const current = document.current;
  return (
    <div className="graph-local-hero">
      <header>
        <span>Graph-local Hero</span>
        <code>{heroId}</code>
        {saving ? <small>Saving…</small> : dirty ? <small>Unsaved</small> : <small>Saved</small>}
      </header>
      <label>Model
        <select value={current.model.ref} onChange={(event) => update((doc) => ({ ...doc, model: { ...doc.model, ref: event.target.value } }))}>
          {models.map((option) => <option key={option.ref} value={option.ref}>{option.label}</option>)}
        </select>
      </label>
      <label>Prompt
        <textarea value={current.prompt} onChange={(event) => update((doc) => ({ ...doc, prompt: event.target.value }))} />
      </label>
      <label>Allowed tools
        <input value={current.allowedTools.join(", ")}
          onChange={(event) => update((doc) => ({ ...doc, allowedTools: parseToolList(event.target.value) }))} />
      </label>
      <label>Authority
        <select value={current.authority}
          onChange={(event) => update((doc) => ({ ...doc, authority: event.target.value as PortableHeroDocument["authority"] }))}>
          <option value="read_only">read_only</option>
          <option value="write_workspace">write_workspace</option>
        </select>
      </label>
      <p>Context, delegation limits and the report contract keep the defaults this Hero was created with.</p>
    </div>
  );
}
