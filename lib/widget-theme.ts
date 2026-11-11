/**
 * The theme bridge: Ennote's semantic tokens, renamed to what a widget guide would
 * reasonably expect a host to provide.
 *
 * The values are read from the *computed* style, so `var()` chains are already
 * resolved and a widget receives concrete colors it can use directly — there is no
 * second palette to keep in sync, and no need to rebuild the frame document when
 * the theme changes.
 *
 * Direction is one-way and value-only. The frame never reads our CSS and never
 * declares a policy; it receives a map of custom properties.
 */

/** One entry per custom property the frame is given. */
export const WIDGET_THEME_VARS: ReadonlyArray<{ name: string; token: string }> = [
  { name: "--color-background-primary", token: "--surface-0" },
  { name: "--color-background-secondary", token: "--surface-1" },
  { name: "--color-background-tertiary", token: "--surface-2" },
  { name: "--color-text-primary", token: "--content-1" },
  { name: "--color-text-secondary", token: "--content-2" },
  { name: "--color-text-tertiary", token: "--content-3" },
  { name: "--color-border-primary", token: "--stroke-strong" },
  { name: "--color-border-secondary", token: "--stroke-hairline" },
  { name: "--color-border-tertiary", token: "--stroke-hairline" },
  { name: "--color-accent", token: "--accent" },
  { name: "--color-background-success", token: "--success-soft" },
  { name: "--color-background-warning", token: "--warning-soft" },
  { name: "--color-background-danger", token: "--danger-soft" },
  { name: "--color-background-info", token: "--info-soft" },
  { name: "--color-text-success", token: "--success" },
  { name: "--color-text-warning", token: "--warning" },
  { name: "--color-text-danger", token: "--danger" },
  { name: "--color-text-info", token: "--info" },
  { name: "--color-chart-1", token: "--data-cat-system" },
  { name: "--color-chart-2", token: "--data-cat-hero" },
  { name: "--color-chart-3", token: "--data-cat-rules" },
  { name: "--color-chart-4", token: "--data-cat-skills" },
  { name: "--color-chart-5", token: "--data-cat-tools" },
  { name: "--color-chart-6", token: "--data-cat-reminders" },
  { name: "--font-sans", token: "--font-sans" },
  { name: "--font-mono", token: "--font-mono" },
  { name: "--radius-sm", token: "--radius-sm" },
  { name: "--radius-md", token: "--radius-md" },
  { name: "--radius-lg", token: "--radius-lg" },
];

/**
 * Resolve the map from a token reader. A token the host does not define is omitted
 * rather than sent empty, so the frame keeps the fallback it declared instead of
 * receiving a property that resolves to nothing.
 */
export function resolveWidgetThemeVars(read: (token: string) => string): Record<string, string> {
  const vars: Record<string, string> = {};
  for (const { name, token } of WIDGET_THEME_VARS) {
    const value = read(token).trim();
    if (value) vars[name] = value;
  }
  return vars;
}

/** The same map, read from the live document. Browser only. */
export function widgetThemeVarsFromDocument(): Record<string, string> {
  if (typeof window === "undefined" || typeof document === "undefined") return {};
  const computed = getComputedStyle(document.documentElement);
  return resolveWidgetThemeVars(token => computed.getPropertyValue(token));
}
