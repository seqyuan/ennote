export type Locale = "en" | "zh-CN";

export const LOCALES: readonly Locale[] = ["en", "zh-CN"];

const STORAGE_KEY = "ennote-locale";

/** Read the persisted locale ("en" fallback; fixed default, not browser-derived). */
export function readLocale(): Locale {
  if (typeof window === "undefined") return "en";
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY);
    if (stored === "en" || stored === "zh-CN") return stored;
  } catch { /* unavailable storage: fall through to the default */ }
  return "en";
}

/** Persist the active locale. */
export function writeLocale(locale: Locale): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, locale);
  } catch { /* unavailable storage: preference is best-effort */ }
}

/** Reflect the locale onto the document element (screen readers + font selection). */
export function applyLocaleAttribute(locale: Locale): void {
  if (typeof document !== "undefined") document.documentElement.lang = locale;
}
