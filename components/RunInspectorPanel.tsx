"use client";

/**
 * RunInspectorPanel: the read-only surface for what the Worker actually gave
 * the model in one Run.
 *
 * It renders the frozen composition the Worker recorded before the first
 * Provider request — never a re-derivation from current settings. That
 * distinction is the point: a Role version, a project instruction file, or a
 * preloaded Skill can change at any time, but what *this* Run saw cannot.
 */

import { useState } from "react";
import { ChevronDown, ChevronRight, FileClock } from "lucide-react";
import { useT } from "@/components/LocaleProvider";
import { formatBytes, shortDigest } from "@/lib/run-inspection";
import type { RunPromptComposition } from "@/hooks/useRunPromptComposition";
import type { components } from "@/lib/worker-api.gen";

type PromptSection = components["schemas"]["PromptSection"];

const KIND_LABELS: Record<string, string> = {
  base: "Platform",
  role: "Role",
  context: "Project context",
  skills_catalog: "Skill catalog",
  skill_preload: "Preloaded skill",
};

const HEADING: React.CSSProperties = {
  fontSize: 10, fontWeight: 600, letterSpacing: 0.4, textTransform: "uppercase",
  color: "var(--text-dim)", marginBottom: 6,
};

const MUTED: React.CSSProperties = { color: "var(--text-dim)", fontSize: 11, lineHeight: 1.5 };

const DIGEST: React.CSSProperties = {
  fontFamily: "var(--font-mono, ui-monospace, monospace)", fontSize: 10.5, color: "var(--text-dim)",
  overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap",
};

/** Presentational body: pure props, so its states are directly testable. */
export function RunInspectorBody({ runId, composition, loading, error, t }: {
  runId: string | null;
  composition: RunPromptComposition | null;
  loading: boolean;
  error: string | null;
  t: (key: string) => string;
}) {
  const [promptOpen, setPromptOpen] = useState(false);

  if (!runId) return <div style={{ ...MUTED, padding: 18 }}>{t("inspector.noRun")}</div>;
  if (loading && !composition) return <div style={{ ...MUTED, padding: 18 }}>{t("inspector.loading")}</div>;
  if (error) return <div style={{ ...MUTED, padding: 18 }}>{error}</div>;
  if (!composition) return <div style={{ ...MUTED, padding: 18 }}>{t("inspector.loading")}</div>;

  const sections = composition.sections ?? [];
  const recorded = composition.recorded;

  return <div style={{ display: "flex", flexDirection: "column", gap: 14, padding: "14px 16px 20px", overflowY: "auto" }}>
    <div>
      <div style={HEADING}>{t("inspector.run")}</div>
      <div style={{ ...DIGEST, marginBottom: 4 }} title={runId}>{runId}</div>
      <div style={MUTED}>{recorded ? t("inspector.frozen") : t("inspector.unrecorded")}</div>
    </div>

    {recorded && <div>
      <div style={HEADING}>
        {t("inspector.composition")}
        <span style={{ marginLeft: 6, fontWeight: 400, textTransform: "none", letterSpacing: 0 }}>
          {t("inspector.sections").replace("{count}", String(sections.length))}
        </span>
      </div>
      <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
        {sections.map((section) => <SectionRow key={section.id} section={section} />)}
      </div>
    </div>}

    {/* The base-prompt digest belongs to every Run that froze a prompt, so it
        stays readable even when the composition itself was never recorded:
        the reader learns what identity the Run is bound to either way. */}
    <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
      <DigestLine label={t("inspector.sectionsDigest")} value={composition.sectionsDigest} />
      <DigestLine label={t("inspector.composedDigest")} value={composition.composedDigest} />
      <DigestLine label={t("inspector.baseDigest")} value={composition.digest} />
      {composition.skillCatalogState !== "" && <div style={{ display: "flex", gap: 8, fontSize: 10.5 }}>
        <span style={{ ...MUTED, flexShrink: 0 }}>{t("inspector.skillCatalog")}</span>
        <span style={{ ...MUTED, color: "var(--text)" }}>
          {t(`inspector.catalog.${composition.skillCatalogState}`)}
        </span>
        {composition.skillCatalogDigest !== "" && <span style={{ ...DIGEST, minWidth: 0 }} title={composition.skillCatalogDigest}>
          {shortDigest(composition.skillCatalogDigest, 8)}
        </span>}
      </div>}
    </div>

    {recorded && <div>
      <button
        type="button"
        onClick={() => setPromptOpen((value) => !value)}
        aria-expanded={promptOpen}
        style={{
          display: "flex", alignItems: "center", gap: 6, width: "100%", padding: "6px 0",
          background: "none", border: "none", cursor: "pointer", color: "var(--text)",
          fontSize: 11.5, fontWeight: 600, textAlign: "left",
        }}
      >
        {promptOpen ? <ChevronDown size={13} aria-hidden="true" /> : <ChevronRight size={13} aria-hidden="true" />}
        <FileClock size={13} aria-hidden="true" />
        {t("inspector.prompt")}
        <span style={{ fontWeight: 400, color: "var(--text-dim)" }}>
          {formatBytes(composition.prompt.length)}
        </span>
      </button>
      {promptOpen && <pre
        data-run-prompt
        style={{
          margin: "6px 0 0", padding: 10, borderRadius: 6, border: "1px solid var(--border)",
          background: "var(--bg-panel)", fontSize: 10.5, lineHeight: 1.45,
          whiteSpace: "pre-wrap", wordBreak: "break-word", maxHeight: 420, overflowY: "auto",
        }}
      >{composition.prompt}</pre>}
    </div>}
  </div>;
}

function SectionRow({ section }: { section: PromptSection }) {
  return <div
    data-prompt-section={section.kind}
    style={{
      display: "flex", alignItems: "center", gap: 8, padding: "5px 8px",
      border: "1px solid var(--border)", borderRadius: 6, fontSize: 11,
    }}
  >
    <span style={{
      flexShrink: 0, fontSize: 9.5, fontWeight: 600, padding: "1px 5px", borderRadius: 4,
      border: "1px solid var(--border)", color: "var(--text-dim)",
    }}>{KIND_LABELS[section.kind] ?? section.kind}</span>
    <span style={{ flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
      <span style={{ color: "var(--text)" }}>{section.id}</span>
      <span style={{ color: "var(--text-dim)" }}>{` · ${section.source}`}</span>
    </span>
    <span style={{ flexShrink: 0, color: "var(--text-dim)", fontVariantNumeric: "tabular-nums" }}>
      {formatBytes(section.bytes)}
    </span>
    <span style={{ ...DIGEST, flexShrink: 0, width: 62 }} title={section.digest}>{shortDigest(section.digest, 8)}</span>
  </div>;
}

function DigestLine({ label, value }: { label: string; value: string }) {
  if (!value) return null;
  return <div style={{ display: "flex", gap: 8, fontSize: 10.5 }}>
    <span style={{ ...MUTED, flexShrink: 0 }}>{label}</span>
    <span style={{ ...DIGEST, minWidth: 0 }} title={value}>{value}</span>
  </div>;
}

export function RunInspectorPanel({ runId, composition, loading, error }: {
  runId: string | null;
  composition: RunPromptComposition | null;
  loading: boolean;
  error: string | null;
}) {
  const t = useT();
  return <RunInspectorBody runId={runId} composition={composition} loading={loading} error={error} t={t} />;
}
