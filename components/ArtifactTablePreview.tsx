"use client";

import { useEffect, useState } from "react";
import type { ArtifactReference, TablePreview } from "@/lib/artifacts";
import { artifactPreviewURL } from "@/lib/artifacts";
import { apiFetch } from "@/lib/worker-api.client";

export function ArtifactTablePreview({ sessionId, artifact }: { sessionId: string; artifact: ArtifactReference }) {
  const [sheet, setSheet] = useState("");
  const [preview, setPreview] = useState<TablePreview | null>(null);
  const [error, setError] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    void apiFetch<TablePreview>(artifactPreviewURL(sessionId, artifact.artifactId, sheet || undefined), {
      signal: controller.signal,
    }).then(value => {
      setError(false);
      setPreview(value);
    }).catch(reason => {
      if ((reason as Error).name !== "AbortError") setError(true);
    });
    return () => controller.abort();
  }, [artifact.artifactId, sessionId, sheet]);

  if (!preview && !error) return <div className="artifact-preview-state" role="status" aria-live="polite">Loading preview...</div>;
  if (error || !preview) return <div className="artifact-preview-state is-error" role="alert">Preview unavailable</div>;

  return <div className="artifact-table-preview">
    {(preview.sheets?.length ?? 0) > 1 && <label className="artifact-sheet-control">
      <span>Worksheet</span>
      <select value={sheet || preview.sheet || ""} onChange={event => setSheet(event.target.value)}>
        {preview.sheets?.map(name => <option value={name} key={name}>{name}</option>)}
      </select>
    </label>}
    <div className="artifact-table-scroll" tabIndex={0} aria-label={`Scrollable preview of ${artifact.name}`}>
      <table>
        <caption className="sr-only">Preview of {artifact.name}</caption>
        <thead><tr>{preview.columns.map((column, index) =>
          <th scope="col" title={column} key={`${column}-${index}`}>{column || `Column ${index + 1}`}</th>)}</tr></thead>
        <tbody>{preview.rows.map((row, rowIndex) => <tr key={rowIndex}>
          {preview.columns.map((_, columnIndex) => <td title={row[columnIndex] ?? ""} key={columnIndex}>
            {row[columnIndex] ?? ""}
          </td>)}
        </tr>)}</tbody>
      </table>
    </div>
    {(preview.truncatedRows || preview.truncatedColumns) && <div className="artifact-preview-limit">
      Preview limited to {preview.rowLimit} rows x {preview.columnLimit} columns
    </div>}
  </div>;
}
