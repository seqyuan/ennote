/**
 * What the settings search matches, and why English stays a haystack.
 *
 * The nav rows are translated (0be35ad), so a search over `label`/`description`
 * alone only ever finds the active locale's wording: in a Chinese UI typing
 * `models` started answering "No settings match" for a section that is right
 * there. The English text is therefore searched alongside the localized text in
 * every locale — the query language is not part of the interface language.
 *
 * A tab contributes three haystacks: its localized label, its localized
 * description, and the English label + description they were translated from.
 */
import { translate, type Translator } from "./messages";

export interface SettingsTabSearch {
  /** Localized label; also what the nav row shows. */
  label: string;
  /** Localized description. */
  description: string;
  /** English label and description, searched whatever the active locale is. */
  englishHaystack: string;
}

/**
 * The searchable text of one tab from the message keys that describe it. Both
 * locales are read here, so a caller cannot hand over the localized text without
 * the English baseline it must be searchable by.
 */
export function settingsTabSearch(t: Translator, labelKey: string, descriptionKey: string): SettingsTabSearch {
  return {
    label: t(labelKey),
    description: t(descriptionKey),
    englishHaystack: `${translate("en", labelKey)} ${translate("en", descriptionKey)}`,
  };
}

/** Whether the tab matches a raw query; a blank query matches everything. */
function matchesSettingsQuery(tab: SettingsTabSearch, query: string): boolean {
  const needle = query.trim().toLowerCase();
  if (needle === "") return true;
  return `${tab.label} ${tab.description} ${tab.englishHaystack}`.toLowerCase().includes(needle);
}

/** The tabs a query leaves; a blank query keeps the whole list in order. */
export function filterSettingsTabs<T extends SettingsTabSearch>(tabs: readonly T[], query: string): T[] {
  return tabs.filter((tab) => matchesSettingsQuery(tab, query));
}
