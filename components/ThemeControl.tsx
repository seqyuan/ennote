"use client";

import { Moon, Sun } from "lucide-react";
import { useTheme } from "@/hooks/useTheme";

/**
 * Single-button theme toggle (annodex style): clicking switches between light
 * and dark with the circular wipe transition. No "system" picker — the first
 * visit still follows the OS preference until the user chooses explicitly.
 */
export function ThemeControl() {
  const { isDark, toggleTheme } = useTheme();
  return (
    <button
      type="button"
      className="topbar-icon-button"
      onClick={(event) => {
        const rect = event.currentTarget.getBoundingClientRect();
        toggleTheme({ x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 });
      }}
      title={isDark ? "Switch to light mode" : "Switch to dark mode"}
      aria-label={isDark ? "Switch to light mode" : "Switch to dark mode"}
      aria-pressed={isDark}
    >
      {isDark ? <Sun size={15} aria-hidden="true" /> : <Moon size={15} aria-hidden="true" />}
    </button>
  );
}
