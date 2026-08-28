/** A session with no leaf has never received a turn — dsh's `blank` flag. */
export function isBlankSession(session: { activeLeafMessageId?: string | null } | null | undefined): boolean {
  return session != null && !session.activeLeafMessageId;
}
