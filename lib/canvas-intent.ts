/**
 * "Open this artifact in the canvas", as an intent rather than a prop.
 *
 * The button lives in `ArtifactView`, five components below the only thing that
 * can satisfy it — the right panel's state, which `AppShell` owns through
 * `useProjectFiles`. Threading a callback down that chain (ChatWindow →
 * ConversationTimeline → ConversationTurn → ToolCallView → ArtifactView) would
 * add a parameter to four components that do not care about the canvas, several
 * of which sit at their line budget. A named event keeps the wish where it is
 * made and the decision where the state is.
 *
 * The shape is deliberately not "the panel's state": the message timeline knows
 * a session and an artifact, and nothing about panes, Projects or reloads.
 *
 * **A subscriber must check `sessionId` against the session it is showing.**
 * Every event in this app is scoped by its owner (AGENTS.md): a message can
 * still be on screen (or the button can be pressed) while another session is
 * being opened, and an intent from the session that was left must not put its
 * artifact into the current Project's lane — the row it would create names an
 * artifact whose bytes the current session cannot even authorize.
 */

export const OPEN_ARTIFACT_EVENT = "ennote:open-artifact";

export interface OpenArtifactIntent {
  /** The session whose transcript produced the artifact. Half of a row's id. */
  sessionId: string;
  artifactId: string;
  name: string;
}

/** Ask the shell to show this artifact in the canvas. */
export function requestOpenArtifact(intent: OpenArtifactIntent): void {
  if (typeof window === "undefined") return;
  window.dispatchEvent(new CustomEvent<OpenArtifactIntent>(OPEN_ARTIFACT_EVENT, { detail: intent }));
}

/**
 * Listen for those requests.
 *
 * The payload is re-read field by field instead of trusted: it arrives as
 * `event.detail`, which is `any` by construction, and a row built from a
 * half-filled intent would be persisted as an artifact tab that opens nothing.
 */
export function subscribeOpenArtifact(handler: (intent: OpenArtifactIntent) => void): () => void {
  if (typeof window === "undefined") return () => {};
  const listener = (event: Event) => {
    const detail = (event as CustomEvent<Partial<OpenArtifactIntent> | null>).detail;
    if (!detail) return;
    const { sessionId, artifactId, name } = detail;
    if (typeof sessionId !== "string" || sessionId === "") return;
    if (typeof artifactId !== "string" || artifactId === "") return;
    handler({ sessionId, artifactId, name: typeof name === "string" ? name : "" });
  };
  window.addEventListener(OPEN_ARTIFACT_EVENT, listener);
  return () => window.removeEventListener(OPEN_ARTIFACT_EVENT, listener);
}
