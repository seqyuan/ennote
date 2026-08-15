import type { Locale } from "./locale";

/** Minimal message dictionary. Full UI translation is a follow-up; this proves
 *  the locale mechanism on the General settings page and the language row. */
const messages: Record<Locale, Record<string, string>> = {
  en: {
    "general.title": "General",
    "general.desc": "Appearance and workspace-level preferences.",
    "general.appearance": "Appearance",
    "general.permission": "Default permission",
    "general.permission.desc": "Permission preset applied to newly created sessions.",
    "general.language": "Language",
    "language.en": "English",
    "language.zh": "中文",
  },
  "zh-CN": {
    "general.title": "通用",
    "general.desc": "外观与工作区偏好设置。",
    "general.appearance": "外观",
    "general.permission": "默认权限",
    "general.permission.desc": "应用于新建会话的权限预设。",
    "general.language": "语言",
    "language.en": "English",
    "language.zh": "中文",
  },
};

/** Translate a message key for the given locale (English fallback). */
export function translate(locale: Locale, key: string): string {
  return messages[locale]?.[key] ?? messages.en[key] ?? key;
}
