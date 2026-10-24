"use client";

import { Monitor, Moon, Sun } from "lucide-react";
import { useState } from "react";
import { useLocale } from "@/components/LocaleProvider";
import { useTheme } from "@/hooks/useTheme";
import { useSettingsStatus } from "@/components/settings/SettingsStatus";
import { SettingsSection } from "@/components/settings/SettingsPrimitives";
import { readDefaultPermissionMode, writeDefaultPermissionMode } from "@/lib/default-permission";
import { LOCALES } from "@/lib/locale";
import { translate } from "@/lib/messages";
import type { PermissionMode } from "@/lib/permission-mode";

const PERMISSION_MODES: readonly PermissionMode[] = ["discuss", "ask", "auto"];

export function GeneralSettings() {
  const { mode, setThemeMode } = useTheme();
  const { locale, setLocale } = useLocale();
  const [defaultPermission, setDefaultPermission] = useState<PermissionMode>(() => readDefaultPermissionMode());
  const { markSaved } = useSettingsStatus();
  const t = (key: string) => translate(locale, key);
  const themeOptions = [
    { id: "light" as const, label: "Light", icon: <Sun size={15} aria-hidden="true" /> },
    { id: "dark" as const, label: "Dark", icon: <Moon size={15} aria-hidden="true" /> },
    { id: "system" as const, label: "System", icon: <Monitor size={15} aria-hidden="true" /> },
  ];

  return (
    <section className="settings-tab-section" aria-labelledby="settings-general-heading">
      <header><h2 id="settings-general-heading">{t("general.title")}</h2>
        <p>{t("general.desc")}</p></header>
      <SettingsSection title={t("general.appearance")} className="appearance-group">
        <div className="appearance-cubes" role="group" aria-label={t("general.appearance")}>
          {themeOptions.map((option) => (
            <button
              key={option.id}
              type="button"
              className={`appearance-cube ${mode === option.id ? "selected" : ""}`}
              aria-pressed={mode === option.id}
              onClick={() => { setThemeMode(option.id); markSaved(); }}
            >
              {option.icon}
              <span>{option.label}</span>
            </button>
          ))}
        </div>
      </SettingsSection>
      <SettingsSection title={t("general.permission")} description={t("general.permission.desc")} className="permission-group">
        <div className="appearance-cubes" role="group" aria-label={t("general.permission")}>
          {PERMISSION_MODES.map((permission) => (
            <button
              key={permission}
              type="button"
              className={`appearance-cube ${defaultPermission === permission ? "selected" : ""}`}
              aria-pressed={defaultPermission === permission}
              onClick={() => {
                setDefaultPermission(permission);
                writeDefaultPermissionMode(permission);
                markSaved();
              }}
            >
              <span>{permission === "discuss" ? "Discuss" : permission === "ask" ? "Ask" : "Auto"}</span>
            </button>
          ))}
        </div>
      </SettingsSection>
      <SettingsSection title={t("general.language")} className="language-group">
        <div className="appearance-cubes" role="group" aria-label={t("general.language")}>
          {LOCALES.map((lang) => (
            <button
              key={lang}
              type="button"
              className={`appearance-cube ${locale === lang ? "selected" : ""}`}
              aria-pressed={locale === lang}
              onClick={() => { setLocale(lang); markSaved(); }}
            >
              <span>{lang === "en" ? t("language.en") : t("language.zh")}</span>
            </button>
          ))}
        </div>
      </SettingsSection>
    </section>
  );
}
