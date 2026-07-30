// Flat monochrome file & folder icons — all use currentColor / var(--text-dim)

interface IconProps {
  size?: number;
}

const DIM = "var(--text-dim)";

// ── Folder ──

export function FolderIcon({ size = 14, open = false }: IconProps & { open?: boolean }) {
  if (open) {
    return (
      <svg width={size} height={size} viewBox="0 0 16 16" fill="none">
        <path d="M1 4.5A1 1 0 0 1 2 3.5H5.5L7 5h7.5v1H1V4.5Z" fill={DIM} />
        <path d="M1 6h14.5L14 13H2L1 6Z" stroke={DIM} strokeWidth="1" fill={DIM} fillOpacity="0.12" />
      </svg>
    );
  }
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none">
      <path d="M1 4.5A1 1 0 0 1 2 3.5H5.5L7 5H14a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1H2a1 1 0 0 1-1-1V4.5Z"
        stroke={DIM} strokeWidth="1" fill={DIM} fillOpacity="0.1" />
    </svg>
  );
}

// ── Generic file ──

export function GenericFileIcon({ size = 14 }: IconProps) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none">
      <path d="M3 2h7l3 3v9H3V2Z" stroke={DIM} strokeWidth="1" fill={DIM} fillOpacity="0.08" />
      <path d="M10 2v3h3" stroke={DIM} strokeWidth="1" fill="none" strokeLinejoin="round" />
    </svg>
  );
}

// ── Label file icon ──

function LabelFileIcon({ label, size = 14 }: { label: string; size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 14 14" fill="none">
      <path d="M2.5 1h6l3 3v9h-9V1Z" stroke={DIM} strokeWidth="0.9" fill={DIM} fillOpacity="0.07" strokeLinejoin="round" />
      <path d="M8.5 1v3h3" stroke={DIM} strokeWidth="0.9" fill="none" strokeLinejoin="round" />
      <text x="7" y="9.5" textAnchor="middle" fontSize="3.4" fontFamily="var(--font-mono), monospace" fontWeight="600" fill={DIM}>{label}</text>
    </svg>
  );
}

// ── Type-specific icons ──

export function PythonIcon({ size = 14 }: IconProps) { return <LabelFileIcon label="PY" size={size} />; }
export function RIcon({ size = 14 }: IconProps) { return <LabelFileIcon label="R" size={size} />; }
export function TypeScriptIcon({ size = 14 }: IconProps) { return <LabelFileIcon label="TS" size={size} />; }
export function JavaScriptIcon({ size = 14 }: IconProps) { return <LabelFileIcon label="JS" size={size} />; }
export function JsonIcon({ size = 14 }: IconProps) { return <LabelFileIcon label="{}" size={size} />; }
export function CssIcon({ size = 14 }: IconProps) { return <LabelFileIcon label="CSS" size={size} />; }
export function HtmlIcon({ size = 14 }: IconProps) { return <LabelFileIcon label="HTM" size={size} />; }
export function MarkdownIcon({ size = 14 }: IconProps) {
  return (
    <svg width={size} height={size} viewBox="0 0 14 14" fill="none">
      <path d="M2.5 1h6l3 3v9h-9V1Z" stroke={DIM} strokeWidth="0.9" fill={DIM} fillOpacity="0.07" strokeLinejoin="round" />
      <path d="M8.5 1v3h3" stroke={DIM} strokeWidth="0.9" fill="none" strokeLinejoin="round" />
      <path d="M3.5 9.5V7l1.5 1.5L6.5 7v2.5" stroke={DIM} strokeWidth="0.9" strokeLinecap="round" strokeLinejoin="round" fill="none" />
      <path d="M8 7v2.5M7 9l1 1.5 1-1.5" stroke={DIM} strokeWidth="0.9" strokeLinecap="round" strokeLinejoin="round" fill="none" />
    </svg>
  );
}
export function YamlIcon({ size = 14 }: IconProps) { return <LabelFileIcon label="YML" size={size} />; }
export function ShellIcon({ size = 14 }: IconProps) {
  return (
    <svg width={size} height={size} viewBox="0 0 14 14" fill="none">
      <path d="M2.5 1h6l3 3v9h-9V1Z" stroke={DIM} strokeWidth="0.9" fill={DIM} fillOpacity="0.07" strokeLinejoin="round" />
      <path d="M8.5 1v3h3" stroke={DIM} strokeWidth="0.9" fill="none" strokeLinejoin="round" />
      <path d="M4 7.5l2 1.5-2 1.5" stroke={DIM} strokeWidth="0.95" strokeLinecap="round" strokeLinejoin="round" fill="none" />
      <path d="M7.5 10.5h2.5" stroke={DIM} strokeWidth="0.95" strokeLinecap="round" />
    </svg>
  );
}
export function GoIcon({ size = 14 }: IconProps) { return <LabelFileIcon label="GO" size={size} />; }
export function RustIcon({ size = 14 }: IconProps) { return <LabelFileIcon label="RS" size={size} />; }
export function SqlIcon({ size = 14 }: IconProps) { return <LabelFileIcon label="SQL" size={size} />; }
export function DockerfileIcon({ size = 14 }: IconProps) {
  return (
    <svg width={size} height={size} viewBox="0 0 14 14" fill="none">
      <path d="M2.5 1h6l3 3v9h-9V1Z" stroke={DIM} strokeWidth="0.9" fill={DIM} fillOpacity="0.07" strokeLinejoin="round" />
      <path d="M8.5 1v3h3" stroke={DIM} strokeWidth="0.9" fill="none" strokeLinejoin="round" />
      <rect x="3.5" y="6.5" width="2" height="1.5" rx="0.3" stroke={DIM} strokeWidth="0.8" />
      <rect x="6" y="6.5" width="2" height="1.5" rx="0.3" stroke={DIM} strokeWidth="0.8" />
      <rect x="3.5" y="8.5" width="2" height="1.5" rx="0.3" stroke={DIM} strokeWidth="0.8" />
    </svg>
  );
}
export function ConfigIcon({ size = 14 }: IconProps) {
  return (
    <svg width={size} height={size} viewBox="0 0 14 14" fill="none">
      <path d="M2.5 1h6l3 3v9h-9V1Z" stroke={DIM} strokeWidth="0.9" fill={DIM} fillOpacity="0.07" strokeLinejoin="round" />
      <path d="M8.5 1v3h3" stroke={DIM} strokeWidth="0.9" fill="none" strokeLinejoin="round" />
      <circle cx="7" cy="8.5" r="1.3" stroke={DIM} strokeWidth="0.9" />
      <path d="M7 6.5v.7M7 10.3v.7M5 8.5h.7M8.3 8.5H9M5.5 6.9l.5.5M8.5 9.6l-.5-.5M5.5 10.1l.5-.5M8.5 7.4l-.5.5" stroke={DIM} strokeWidth="0.8" strokeLinecap="round" />
    </svg>
  );
}

// ── Main resolver ──

export function getFileIcon(name: string, size = 14): React.ReactNode {
  const lower = name.toLowerCase();
  const ext = lower.split(".").pop() ?? "";

  if (lower === "dockerfile" || lower.startsWith("dockerfile.")) return <DockerfileIcon size={size} />;
  if (ext === "py") return <PythonIcon size={size} />;
  if (ext === "r" || ext === "rds" || ext === "qs") return <RIcon size={size} />;
  if (ext === "ts") return <TypeScriptIcon size={size} />;
  if (ext === "tsx") return <LabelFileIcon label="TSX" size={size} />;
  if (ext === "js" || ext === "mjs" || ext === "cjs") return <JavaScriptIcon size={size} />;
  if (ext === "jsx") return <LabelFileIcon label="JSX" size={size} />;
  if (ext === "json" || ext === "jsonl") return <JsonIcon size={size} />;
  if (ext === "css" || ext === "less") return <CssIcon size={size} />;
  if (ext === "scss") return <LabelFileIcon label="SC" size={size} />;
  if (ext === "html" || ext === "htm") return <HtmlIcon size={size} />;
  if (ext === "md" || ext === "mdx") return <MarkdownIcon size={size} />;
  if (ext === "yaml" || ext === "yml") return <YamlIcon size={size} />;
  if (ext === "toml") return <LabelFileIcon label="TOM" size={size} />;
  if (ext === "sh" || ext === "bash" || ext === "zsh" || ext === "fish") return <ShellIcon size={size} />;
  if (ext === "rs") return <RustIcon size={size} />;
  if (ext === "go") return <GoIcon size={size} />;
  if (ext === "sql") return <SqlIcon size={size} />;
  if (ext === "tf" || ext === "hcl") return <LabelFileIcon label="TF" size={size} />;
  if (ext === "lock") return <LabelFileIcon label="LK" size={size} />;
  if (ext.endsWith("config.ts") || ext.endsWith("config.js") || ext.endsWith("config.mjs")) return <ConfigIcon size={size} />;

  return <GenericFileIcon size={size} />;
}
