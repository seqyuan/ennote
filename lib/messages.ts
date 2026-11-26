import type { Locale } from "./locale";
import { en } from "./locales/en";
import { zhCN } from "./locales/zh-CN";

/**
 * Every shipped locale's dictionary, keyed by locale.
 *
 * The translations themselves live under ./locales — one file per locale — so a
 * dictionary edit is one locale's diff and no file carries the whole app's copy.
 */
export const messages: Record<Locale, Record<string, string>> = {
  en,
  "zh-CN": zhCN,
};

/** Values a message may interpolate: `{name}` is replaced verbatim. */
export type MessageParams = Record<string, string | number>;

/** A translator bound to one locale. */
export type Translator = (key: string, params?: MessageParams) => string;

/**
 * Translate a message key for the given locale (English fallback) and fill its
 * `{placeholder}` tokens.
 *
 * Interpolation lives here rather than at each call site so the placeholder set
 * is one thing a translation can be wrong about: tests/unit/messages.test.ts
 * requires every locale to carry the same tokens as English, which is the defect
 * a hand-written `.replace` chain cannot catch.
 */
export function translate(locale: Locale, key: string, params?: MessageParams): string {
  const template = messages[locale]?.[key] ?? messages.en[key] ?? key;
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (token, name: string) =>
    Object.hasOwn(params, name) ? String(params[name]) : token);
}

/** The `{placeholder}` names a template uses, de-duplicated and sorted. */
export function placeholders(template: string): string[] {
  const names = new Set<string>();
  for (const match of template.matchAll(/\{(\w+)\}/g)) names.add(match[1]);
  return [...names].sort();
}

