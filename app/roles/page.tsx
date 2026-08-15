"use client";

import { useState } from "react";
import { WorkspacePageShell } from "@/components/WorkspacePageShell";
import { RolesSettings } from "@/components/settings/RolesSettings";
import { useSettingsProfiles } from "@/hooks/useSettingsProfiles";

export default function RolesPage() {
  const settings = useSettingsProfiles();
  const [error, setError] = useState<string | null>(null);
  return (
    <WorkspacePageShell
      title="Roles"
      description="Global reusable execution presets for Chat and Graph Tasks."
      error={error}
      onDismissError={() => setError(null)}
      testId="roles-page"
    >
      <RolesSettings models={settings.models} providers={settings.providers} setError={setError} />
    </WorkspacePageShell>
  );
}
