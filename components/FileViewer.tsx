"use client";

import { Download, FileWarning, LoaderCircle } from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { useEffect, useState } from "react";
import { fetchProjectFile, projectFileContentPath } from "@/lib/project-files";
import { workerAPIPath } from "@/lib/worker-api.client";

interface Props {
  projectId: string;
  filePath: string;
  fileName?: string;
}

type ViewerState =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "markdown"; content: string; size: number }
  | { kind: "code"; content: string; highlighted: string | null; language: string; size: number }
  | { kind: "image" | "pdf"; url: string; mime: string; size: number }
  | { kind: "unsupported"; mime: string; size: number };

const IMAGE_EXTENSIONS = new Set(["png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "ico", "avif"]);
const MARKDOWN_EXTENSIONS = new Set(["md", "mdx", "rmd", "qmd"]);
const TEXT_EXTENSIONS = new Set([
  "txt", "json", "yaml", "yml", "toml", "csv", "tsv", "log", "ts", "tsx", "js", "jsx", "mjs", "cjs",
  "py", "r", "rb", "go", "rs", "java", "c", "cpp", "h", "hpp", "css", "scss", "less", "html", "htm", "xml",
  "sh", "bash", "zsh", "fish", "ps1", "bat", "sql", "graphql", "gql", "conf", "cfg", "ini", "properties",
]);
const MAX_TEXT_BYTES = 4 << 20;

export function FileViewer({ projectId, filePath, fileName }: Props) {
  const [state, setState] = useState<ViewerState>({ kind: "loading" });
  const name = fileName ?? filePath.split("/").pop() ?? filePath;
  const extension = getExtension(name);

  useEffect(() => {
    const controller = new AbortController();
    let objectURL: string | null = null;

    async function load() {
      setState({ kind: "loading" });
      try {
        const response = await fetchProjectFile(projectId, filePath, controller.signal);
        const contentLength = Number(response.headers.get("Content-Length") ?? 0);
        const mime = response.headers.get("Content-Type")?.split(";", 1)[0] ?? "application/octet-stream";

        if (MARKDOWN_EXTENSIONS.has(extension)) {
          if (contentLength > MAX_TEXT_BYTES) throw new Error("Text preview exceeds the 4 MiB limit");
          const content = await response.text();
          if (!controller.signal.aborted) setState({ kind: "markdown", content, size: content.length });
          return;
        }

        if (isTextFile(name, mime)) {
          if (contentLength > MAX_TEXT_BYTES) throw new Error("Text preview exceeds the 4 MiB limit");
          const content = await response.text();
          const language = shikiLanguage(name);
          let highlighted: string | null = null;
          if (language) {
            try {
              const { codeToHtml } = await import("shiki");
              highlighted = await codeToHtml(content, {
                lang: language,
                themes: { light: "github-light", dark: "github-dark" },
                defaultColor: false,
              });
            } catch {
              highlighted = null;
            }
          }
          if (!controller.signal.aborted) setState({ kind: "code", content, highlighted, language: language ?? "text", size: content.length });
          return;
        }

        if (IMAGE_EXTENSIONS.has(extension) || mime.startsWith("image/")) {
          const blob = await response.blob();
          objectURL = URL.createObjectURL(blob);
          if (!controller.signal.aborted) setState({ kind: "image", url: objectURL, mime, size: blob.size });
          return;
        }

        if (extension === "pdf" || mime === "application/pdf") {
          const blob = await response.blob();
          objectURL = URL.createObjectURL(blob);
          if (!controller.signal.aborted) setState({ kind: "pdf", url: objectURL, mime, size: blob.size });
          return;
        }

        setState({ kind: "unsupported", mime, size: contentLength });
      } catch (reason) {
        if (!controller.signal.aborted) setState({ kind: "error", message: (reason as Error).message });
      }
    }

    void load();
    return () => {
      controller.abort();
      if (objectURL) URL.revokeObjectURL(objectURL);
    };
  }, [extension, filePath, name, projectId]);

  const size = "size" in state ? formatFileSize(state.size) : null;
  const typeLabel = state.kind === "code" ? state.language : extension || state.kind;
  const downloadURL = workerAPIPath(projectFileContentPath(projectId, filePath));

  return (
    <div className="file-viewer">
      <div className="file-viewer-status">
        <span className="file-viewer-type">{typeLabel}</span>
        <span className="file-viewer-path" title={filePath}>{filePath}</span>
        {size && <span>{size}</span>}
        <a className="file-viewer-download" href={downloadURL} download={name} title="Download file" aria-label={`Download ${name}`}>
          <Download size={13} aria-hidden="true" />
        </a>
      </div>

      <div className="file-viewer-body">
        {state.kind === "loading" && (
          <div className="file-viewer-empty"><LoaderCircle className="spin" size={18} />Loading preview...</div>
        )}
        {state.kind === "error" && (
          <div className="file-viewer-empty is-error"><FileWarning size={20} />{state.message}</div>
        )}
        {state.kind === "markdown" && (
          <article className="markdown-body file-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]}>{state.content}</ReactMarkdown></article>
        )}
        {state.kind === "code" && state.highlighted && (
          <div className="file-code" dangerouslySetInnerHTML={{ __html: state.highlighted }} />
        )}
        {state.kind === "code" && !state.highlighted && <pre className="file-code-plain">{state.content}</pre>}
        {state.kind === "image" && (
          // eslint-disable-next-line @next/next/no-img-element
          <div className="file-image-stage"><img src={state.url} alt={name} /></div>
        )}
        {state.kind === "pdf" && <iframe className="file-pdf" src={state.url} title={name} />}
        {state.kind === "unsupported" && (
          <div className="file-viewer-empty"><FileWarning size={20} />Preview is not available for {state.mime}.</div>
        )}
      </div>
    </div>
  );
}

function getExtension(fileName: string): string {
  return fileName.includes(".") ? fileName.toLowerCase().split(".").pop() ?? "" : "";
}

function isTextFile(fileName: string, mime: string): boolean {
  const extension = getExtension(fileName);
  const base = fileName.toLowerCase();
  return mime.startsWith("text/") || TEXT_EXTENSIONS.has(extension) || ["dockerfile", "makefile"].includes(base) || base.startsWith(".env");
}

function shikiLanguage(fileName: string): string | null {
  const extension = getExtension(fileName);
  const languages: Record<string, string> = {
    ts: "typescript", tsx: "tsx", js: "javascript", jsx: "jsx", mjs: "javascript", cjs: "javascript",
    py: "python", r: "r", rb: "ruby", go: "go", rs: "rust", java: "java", c: "c", cpp: "cpp", h: "c", hpp: "cpp",
    css: "css", scss: "scss", less: "less", html: "html", htm: "html", xml: "xml", json: "json", yaml: "yaml", yml: "yaml",
    toml: "toml", sh: "bash", bash: "bash", zsh: "bash", fish: "fish", ps1: "powershell", sql: "sql", graphql: "graphql", gql: "graphql",
  };
  const base = fileName.toLowerCase();
  if (base === "dockerfile") return "dockerfile";
  if (base === "makefile") return "makefile";
  return languages[extension] ?? null;
}

function formatFileSize(bytes: number): string {
  if (!bytes) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
