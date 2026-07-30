"use client";

import { useCallback, useSyncExternalStore } from "react";

export type ThemeMode = "system" | "light" | "dark";
type ResolvedTheme = "light" | "dark";
type ToggleOrigin = { x: number; y: number };

const listeners = new Set<() => void>();

function readMode(): ThemeMode {
  if (typeof window === "undefined") return "system";
  const stored = window.localStorage.getItem("ennote-theme");
  return stored === "light" || stored === "dark" ? stored : "system";
}

function resolveTheme(mode = readMode()): ResolvedTheme {
  if (mode === "dark") return "dark";
  if (mode === "light") return "light";
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function getSnapshot(): string {
  if (typeof window === "undefined") return "system:light";
  const mode = readMode();
  return `${mode}:${resolveTheme(mode)}`;
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  const media = window.matchMedia("(prefers-color-scheme: dark)");
  const notifySystemChange = () => {
    if (readMode() !== "system") return;
    applyResolvedTheme(resolveTheme("system"));
    listeners.forEach((callback) => callback());
  };
  const notifyStorageChange = (event: StorageEvent) => {
    if (event.key !== "ennote-theme") return;
    applyResolvedTheme(resolveTheme());
    listeners.forEach((callback) => callback());
  };
  media.addEventListener("change", notifySystemChange);
  window.addEventListener("storage", notifyStorageChange);
  return () => {
    listeners.delete(listener);
    media.removeEventListener("change", notifySystemChange);
    window.removeEventListener("storage", notifyStorageChange);
  };
}

function applyResolvedTheme(theme: ResolvedTheme) {
  document.documentElement.classList.toggle("dark", theme === "dark");
  document.documentElement.dataset.theme = theme;
}

function persistMode(mode: ThemeMode) {
  if (mode === "system") window.localStorage.removeItem("ennote-theme");
  else window.localStorage.setItem("ennote-theme", mode);
  applyResolvedTheme(resolveTheme(mode));
  listeners.forEach((callback) => callback());
}

function transitionTheme(mode: ThemeMode, origin?: ToggleOrigin) {
  const apply = () => persistMode(mode);
  const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  if (typeof document.startViewTransition !== "function" || reduceMotion) {
    apply();
    return;
  }
  const x = origin?.x ?? window.innerWidth / 2;
  const y = origin?.y ?? window.innerHeight / 2;
  const radius = Math.hypot(Math.max(x, window.innerWidth - x), Math.max(y, window.innerHeight - y));
  const transition = document.startViewTransition(apply);
  void transition.ready.then(() => document.documentElement.animate(
    { clipPath: [`circle(0 at ${x}px ${y}px)`, `circle(${radius}px at ${x}px ${y}px)`] },
    { duration: 420, easing: "cubic-bezier(0.22, 0.61, 0.36, 1)", pseudoElement: "::view-transition-new(root)" },
  )).catch(() => undefined);
}

export function useTheme() {
  const snapshot = useSyncExternalStore(subscribe, getSnapshot, () => "system:light");
  const [mode, resolved] = snapshot.split(":") as [ThemeMode, ResolvedTheme];
  const setThemeMode = useCallback((nextMode: ThemeMode, origin?: ToggleOrigin) => transitionTheme(nextMode, origin), []);
  const toggleTheme = useCallback((origin?: ToggleOrigin) => transitionTheme(resolved === "dark" ? "light" : "dark", origin), [resolved]);
  return { mode, theme: resolved, isDark: resolved === "dark", setThemeMode, toggleTheme };
}
