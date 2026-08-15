"use client";

import { Monitor, Moon, Sun } from "lucide-react";
import { useTheme } from "@/hooks/useTheme";

export function GeneralSettings() {
  const { mode, setThemeMode } = useTheme();
  const options = [
    { id: "light" as const, label: "Light", icon: <Sun size={15} aria-hidden="true" /> },
    { id: "dark" as const, label: "Dark", icon: <Moon size={15} aria-hidden="true" /> },
    { id: "system" as const, label: "System", icon: <Monitor size={15} aria-hidden="true" /> },
  ];
  return (
    <section className="settings-tab-section" aria-labelledby="settings-general-heading">
      <header><h2 id="settings-general-heading">General</h2>
        <p>Appearance and workspace-level preferences.</p></header>
      <section className="settings-subsection appearance-group">
        <div className="appearance-title">Appearance</div>
        <div className="appearance-cubes" role="group" aria-label="Appearance">
          {options.map((option) => (
            <button
              key={option.id}
              type="button"
              className={`appearance-cube ${mode === option.id ? "selected" : ""}`}
              aria-pressed={mode === option.id}
              onClick={() => setThemeMode(option.id)}
            >
              {option.icon}
              <span>{option.label}</span>
            </button>
          ))}
        </div>
      </section>
    </section>
  );
}
