"use client";

import { useSkillCatalog } from "@/hooks/useSkillCatalog";
import { useSkillRoots } from "@/hooks/useSkillRoots";
import { useSkillMarketplace } from "@/hooks/useSkillMarketplace";
import { useSkillRowActions } from "@/hooks/useSkillRowActions";
import {
  SkillCatalogList,
  SkillDiagnostics,
  SkillInstalledList,
  SkillMarketplace,
  SkillRootsPanel,
} from "./SkillPanels";

// SkillsSettings is the pi-web-style skills management surface: a marketplace
// search/install panel plus the installed catalog with enable/disable toggles,
// update checks, and removal. All operations run through the loopback Worker.
export function SkillsSettings({ projectId, setError }: {
  projectId: string | null;
  setError: (value: string | null) => void;
}) {
  const catalog = useSkillCatalog(projectId, setError);
  const roots = useSkillRoots(catalog.setRoots, catalog.load, setError);
  const marketplace = useSkillMarketplace(projectId, catalog.projectTrusted, catalog.load, setError);
  const rowActions = useSkillRowActions(projectId, catalog.setSkills, catalog.load, setError);

  const installed = catalog.skills.filter((skill) => Boolean(skill.install));
  const local = catalog.skills.filter((skill) => !skill.install);

  return <section className="settings-tab-section" aria-labelledby="settings-skills-heading">
    <header>
      <h2 id="settings-skills-heading">Skills</h2>
      <p>Marketplace installs, catalog browsing, and per-skill invocation control.</p>
    </header>

    {/* Skills roots (additional resolution paths) */}
    <SkillRootsPanel roots={catalog.roots} loading={catalog.loading} control={roots} />

    {/* Marketplace search */}
    <SkillMarketplace
      projectId={projectId}
      projectTrusted={catalog.projectTrusted}
      query={marketplace.query}
      setQuery={marketplace.setQuery}
      results={marketplace.results}
      searching={marketplace.searching}
      searched={marketplace.searched}
      installScope={marketplace.installScope}
      setInstallScope={marketplace.setInstallScope}
      installing={marketplace.installing}
      onSearch={marketplace.search}
      onInstall={marketplace.install}
    />

    {/* Installed (annotated) skills */}
    <SkillInstalledList
      installed={installed}
      loading={catalog.loading}
      updateKey={rowActions.updateKey}
      updates={rowActions.updates}
      checking={rowActions.checking}
      toggling={rowActions.toggling}
      removing={rowActions.removing}
      onCheck={rowActions.check}
      onUpdate={rowActions.update}
      onToggle={rowActions.toggle}
      onRemove={rowActions.remove}
    />

    {/* Local (non-installed) catalog skills */}
    <SkillCatalogList local={local} />

    {/* Diagnostics */}
    <SkillDiagnostics diagnostics={catalog.diagnostics} />
  </section>;
}
