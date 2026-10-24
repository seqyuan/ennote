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
import { transcriptEntries, transcriptSummary } from "@/lib/run-transcript";
import type { RunMCPFrozen } from "@/hooks/useRunMCP";
import type { RunMessagePage } from "@/hooks/useRunTranscript";
import type { RunPromptComposition } from "@/hooks/useRunPromptComposition";
import type { components } from "@/lib/worker-api.gen";
import styles from "./RunInspectorPanel.module.css";

type PromptSection = components["schemas"]["PromptSection"];
type RunMCPServerSnapshot = components["schemas"]["RunMCPServerSnapshot"];

const KIND_LABELS: Record<string, string> = {
  base: "Platform",
  role: "Role",
  context: "Project context",
  skills_catalog: "Skill catalog",
  skill_preload: "Preloaded skill",
};

const ROLE_LABEL: Record<string, string> = {
  system: "System", user: "User", assistant: "Assistant", tool: "Tool",
};

/** Presentational body: pure props, so its states are directly testable. */
export type TranscriptSection = {
  messages: RunMessagePage["messages"];
  hasMore: boolean;
  loadingOlder: boolean;
  loadOlder: () => void;
};

export function RunInspectorBody({ runId, composition, mcp, transcript, loading, error, t }: {
  runId: string | null;
  composition: RunPromptComposition | null;
  /** Absent on the composition-only call sites; the MCP block then renders nothing. */
  mcp?: RunMCPFrozen | null;
  /** Absent where the transcript was not requested. */
  transcript?: TranscriptSection | null;
  loading: boolean;
  error: string | null;
  t: (key: string) => string;
}) {
  const [promptOpen, setPromptOpen] = useState(false);

  if (!runId) return <div className={`${styles.empty} ${styles.muted}`}>{t("inspector.noRun")}</div>;
  if (loading && !composition) return <div className={`${styles.empty} ${styles.muted}`}>{t("inspector.loading")}</div>;
  if (error) return <div className={`${styles.empty} ${styles.muted}`}>{error}</div>;
  if (!composition) return <div className={`${styles.empty} ${styles.muted}`}>{t("inspector.loading")}</div>;

  const sections = composition.sections ?? [];
  const recorded = composition.recorded;

  return <div className={styles.body}>
    <div>
      <div className={styles.heading}>{t("inspector.run")}</div>
      <div className={`${styles.digest} ${styles.digestValue} ${styles.runId}`} title={runId}>{runId}</div>
      <div className={styles.muted}>{recorded ? t("inspector.frozen") : t("inspector.unrecorded")}</div>
    </div>

    {recorded && <div>
      <div className={styles.heading}>
        {t("inspector.composition")}
        <span className={styles.headingCount}>
          {t("inspector.sections").replace("{count}", String(sections.length))}
        </span>
      </div>
      <div className={styles.group}>
        {sections.map((section) => <SectionRow key={section.id} section={section} />)}
      </div>
    </div>}

    {/* The base-prompt digest belongs to every Run that froze a prompt, so it
        stays readable even when the composition itself was never recorded:
        the reader learns what identity the Run is bound to either way. */}
    <div className={styles.digestGroup}>
      <DigestLine label={t("inspector.sectionsDigest")} value={composition.sectionsDigest} />
      <DigestLine label={t("inspector.composedDigest")} value={composition.composedDigest} />
      <DigestLine label={t("inspector.baseDigest")} value={composition.digest} />
      {composition.skillCatalogState !== "" && <div className={styles.catalogRow}>
        <span className={`${styles.muted} ${styles.catalogLabel}`}>{t("inspector.skillCatalog")}</span>
        <span className={`${styles.muted} ${styles.catalogValue}`}>
          {t(`inspector.catalog.${composition.skillCatalogState}`)}
        </span>
        {composition.skillCatalogDigest !== "" && <span className={`${styles.digest} ${styles.digestValue}`} title={composition.skillCatalogDigest}>
          {shortDigest(composition.skillCatalogDigest, 8)}
        </span>}
      </div>}
    </div>

    {recorded && mcp !== undefined && <div>
      <div className={styles.heading}>{t("inspector.mcp")}</div>
      {mcp === null || (mcp.servers ?? []).length === 0
        ? <div className={styles.muted}>{t("inspector.mcpNone")}</div>
        : <div className={styles.group}>
          {(mcp.servers ?? []).map((server) => <MCPServerRow key={server.id} server={server} t={t} />)}
        </div>}
    </div>}

    {transcript ? <div>
      <div className={styles.heading}>{t("inspector.transcript")}</div>
      <div className={`${styles.muted} ${styles.transcriptNote}`}>{t("inspector.transcriptNote")}</div>
      {transcript.messages.length === 0
        ? <div className={styles.muted}>{t("inspector.transcriptEmpty")}</div>
        : <div className={styles.group}>
          {transcript.messages.map((message) => <TranscriptRow key={message.id} message={message} t={t} />)}
        </div>}
      {transcript.hasMore && <button
        type="button"
        className={styles.loadOlder}
        disabled={transcript.loadingOlder}
        onClick={transcript.loadOlder}
      >{t("inspector.transcriptOlder")}</button>}
    </div> : null}

    {recorded && <div>
      <button
        type="button"
        className={styles.promptToggle}
        onClick={() => setPromptOpen((value) => !value)}
        aria-expanded={promptOpen}
      >
        {promptOpen ? <ChevronDown size={13} aria-hidden="true" /> : <ChevronRight size={13} aria-hidden="true" />}
        <FileClock size={13} aria-hidden="true" />
        {t("inspector.prompt")}
        <span className={styles.promptBytes}>
          {formatBytes(composition.prompt.length)}
        </span>
      </button>
      {promptOpen && <pre data-run-prompt className={styles.promptPre}>{composition.prompt}</pre>}
    </div>}
  </div>;
}

function SectionRow({ section }: { section: PromptSection }) {
  return <div data-prompt-section={section.kind} className={styles.sectionRow}>
    <span className={styles.sectionKind}>{KIND_LABELS[section.kind] ?? section.kind}</span>
    <span className={styles.sectionIdentity}>
      <span className={styles.sectionId}>{section.id}</span>
      <span className={styles.sectionSource}>{` · ${section.source}`}</span>
    </span>
    <span className={styles.sectionBytes}>{formatBytes(section.bytes)}</span>
    <span className={`${styles.digest} ${styles.sectionDigest}`} title={section.digest}>{shortDigest(section.digest, 8)}</span>
  </div>;
}

function MCPServerRow({ server, t }: { server: RunMCPServerSnapshot; t: (key: string) => string }) {
  const unavailable = server.unavailableReason !== "";
  return <div data-mcp-server={server.bindingId} data-unavailable={unavailable || undefined} className={styles.mcpRow}>
    <div className={styles.mcpHead}>
      <span className={styles.mcpIdentity}>
        <span className={styles.mcpBinding}>{server.bindingId}</span>
        <span className={styles.mcpRevision}>{` · rev ${server.bindingRevision}`}</span>
      </span>
      {server.required && <span className={styles.badge}>{t("inspector.mcpRequired")}</span>}
      <span className={styles.mcpTools}>
        {t("inspector.mcpTools").replace("{count}", String((server.tools ?? []).length))}
      </span>
    </div>
    {unavailable && <div className={styles.mcpUnavailable}>
      {`${t("inspector.mcpUnavailable")}: ${server.unavailableReason}`}
    </div>}
    {server.negotiatedProtocol !== "" && <div className={`${styles.muted} ${styles.mcpProtocol}`}>
      {`${t("inspector.mcpProtocol")}: ${server.negotiatedProtocol}`}
    </div>}
    {server.instructions !== "" && <details className={styles.mcpInstructions}>
      <summary className={styles.muted} title={server.instructionsDigest}>
        {t("inspector.mcpInstructions")}
      </summary>
      <div className={styles.mcpInstructionsBody} data-mcp-instructions>{server.instructions}</div>
    </details>}
    {(server.tools ?? []).length > 0 && <div className={styles.toolList}>
      {(server.tools ?? []).map((tool) => <div key={tool.exposedName} className={styles.toolRow}>
        <span className={`${styles.digest} ${styles.toolName}`} title={tool.exposedName}>{tool.exposedName}</span>
        <span className={styles.toolRisk}>{tool.riskClass}</span>
      </div>)}
    </div>}
  </div>;
}

function TranscriptRow({ message, t }: { message: RunMessagePage["messages"][number]; t: (key: string) => string }) {
  const entries = transcriptEntries(message.content);
  const summary = transcriptSummary(message.content);
  return <details data-transcript-ordinal={message.ordinal} className={styles.transcriptRow}>
    <summary className={styles.transcriptSummary}>
      <span className={`${styles.muted} ${styles.transcriptOrdinal}`}>{message.ordinal}</span>
      <span className={styles.transcriptRole}>
        {ROLE_LABEL[message.role] ?? message.role}
      </span>
      {message.visibility === "private" && <span data-transcript-private className={styles.privateBadge}>
        {t("inspector.transcriptPrivate")}
      </span>}
      <span className={styles.transcriptExcerpt}>
        {summary}
      </span>
    </summary>
    <div className={styles.transcriptBody}>
      {entries.map((entry, index) => <TranscriptEntryView entry={entry} key={index} t={t} />)}
    </div>
  </details>;
}

function TranscriptEntryView({ entry, t }: { entry: ReturnType<typeof transcriptEntries>[number]; t: (key: string) => string }) {
  if (entry.kind === "text") return <div className={styles.entryPre}>{entry.text}</div>;
  if (entry.kind === "thinking") return <div>
    <div className={`${styles.muted} ${styles.entryLabel}`}>{t("inspector.transcriptThinking")}</div>
    <div className={styles.entryPre}>{entry.text}</div>
  </div>;
  if (entry.kind === "image") return <div className={styles.muted}>
    {t("inspector.transcriptImage").replace("{width}", String(entry.width)).replace("{height}", String(entry.height))}
  </div>;
  if (entry.kind === "unknown") return <div className={styles.muted}>
    {t("inspector.transcriptUnknown").replace("{type}", entry.type)}
  </div>;
  if (entry.kind === "tool_call") return <details>
    <summary className={`${styles.muted} ${styles.entrySummary}`}>{`${t("inspector.transcriptCall")} ${entry.name}`}</summary>
    {entry.arguments !== undefined && <div className={`${styles.entryPre} ${styles.entryBlock}`}>{entry.arguments}</div>}
  </details>;
  return <details>
    <summary className={`${styles.muted} ${styles.entrySummary}`} data-error={entry.isError || undefined}>
      {`${entry.isError ? t("inspector.transcriptError") : t("inspector.transcriptResult")} ${entry.name}`}
    </summary>
    <div className={`${styles.entryPre} ${styles.entryBlock}`}>{entry.text}</div>
  </details>;
}

function DigestLine({ label, value }: { label: string; value: string }) {
  if (!value) return null;
  return <div className={styles.digestLine}>
    <span className={`${styles.muted} ${styles.digestLabel}`}>{label}</span>
    <span className={`${styles.digest} ${styles.digestValue}`} title={value}>{value}</span>
  </div>;
}

export function RunInspectorPanel({ runId, composition, mcp, transcript, loading, error }: {
  runId: string | null;
  composition: RunPromptComposition | null;
  mcp: RunMCPFrozen | null;
  transcript: TranscriptSection | null;
  loading: boolean;
  error: string | null;
}) {
  const t = useT();
  return <RunInspectorBody
    runId={runId} composition={composition} mcp={mcp} transcript={transcript}
    loading={loading} error={error} t={t} />;
}
