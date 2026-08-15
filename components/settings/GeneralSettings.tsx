"use client";

import { Monitor, Moon, Sun } from "lucide-react";
import { useState } from "react";
import { useTheme } from "@/hooks/useTheme";
import { readDefaultPermissionMode, writeDefaultPermissionMode } from "@/lib/default-permission";
import type { PermissionMode } from "@/lib/permission-mode";

const PERMISSION_MODES: readonly PermissionMode[] = ["discuss", "ask", "auto"];

export function GeneralSettings() {
  const { mode, setThemeMode } = useTheme();
  const [defaultPermission, setDefaultPermission] = useState<PermissionMode>(() => readDefaultPermissionMode());
  const themeOptions = [
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
          {themeOptions.map((option) => (
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
      <section className="settings-subsection permission-group">
        <div className="appearance-title">Default permission</div>
        <p className="settings-note">Permission preset applied to newly created sessions.</p>
        <div className="appearance-cubes" role="group" aria-label="Default permission">
          {PERMISSION_MODES.map((permission) => (
            <button
              key={permission}
              type="button"
              className={`appearance-cube ${defaultPermission === permission ? "selected" : ""}`}
              aria-pressed={defaultPermission === permission}
              onClick={() => {
                setDefaultPermission(permission);
                writeDefaultPermissionMode(permission);
              }}
            >
              <span>{permission === "discuss" ? "Discuss" : permission === "ask" ? "Ask" : "Auto"}</span>
            </button>
          ))}
        </div>
      </section>
    </section>
  );
}
