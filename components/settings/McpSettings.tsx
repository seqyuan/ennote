"use client";

import { useMcpSettings } from "@/hooks/useMcpSettings";
import { useT } from "@/components/LocaleProvider";
import { SettingsEmpty, SettingsLoading } from "@/components/settings/SettingsPrimitives";
import { McpBindingCard, McpCandidateSection, McpCreateForm } from "./McpPanels";
import styles from "./McpPanels.module.css";

export function McpSettings({ projectId, setError }: {
  projectId: string | null;
  setError: (value: string | null) => void;
}) {
  const t = useT();
  const mcp = useMcpSettings(projectId, setError);

  if (!projectId) {
    return <SettingsEmpty>{t("settings.mcp.openProject")}</SettingsEmpty>;
  }

  return (
    <div className={styles.settingsRoot}>
      {/* Section header */}
      <div className={styles.settingsHeader}>
        <div>
          <h2 id="settings-mcp-heading" className={styles.settingsTitle}>{t("settings.mcp.title")}</h2>
          <p className={styles.settingsDescription}>{t("settings.mcp.desc")}</p>
        </div>
        <button
          type="button"
          onClick={() => mcp.setShowCreate((value) => !value)}
          className="settings-btn"
        >
          {mcp.showCreate ? t("settings.mcp.cancel") : t("settings.mcp.addServer")}
        </button>
      </div>

      <McpCreateForm mcp={mcp} />

      {/* Bindings */}
      <div className={styles.bindings}>
        {mcp.bindings.length === 0 && !mcp.loading && (
          <SettingsEmpty>{t("settings.mcp.empty")}</SettingsEmpty>
        )}
        {mcp.bindings.map((binding) => (
          <McpBindingCard key={binding.id} mcp={mcp} binding={binding} />
        ))}
      </div>

      {/* Discovered candidates (project file, bundled, managed) */}
      <McpCandidateSection mcp={mcp} />
      {mcp.loading && <SettingsLoading label={t("settings.common.loading")} />}
    </div>
  );
}
