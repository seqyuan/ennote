"use client";

import { FileTreePanel } from "../FileTreePanel";

/**
 * The Files channel: the workspace tree, and nothing else.
 *
 * The tree is keyed by Project so a switch remounts it, and it is *not* keyed by
 * anything else. Once the inspector lane exists, the tree must survive every
 * file that opens beside it — that is the whole point of the lane split.
 */
export function FilesChannel(props: {
  projectId: string | null;
  displayPath: string | null;
  onOpenFile: (path: string, name: string) => void;
  onOpenFileToSide: (path: string, name: string) => void;
  onPreviewFile: (path: string, name: string) => void;
}) {
  return (
    <FileTreePanel
      key={props.projectId ?? "no-project"}
      projectId={props.projectId}
      displayPath={props.displayPath}
      onOpenFile={props.onOpenFile}
      onOpenFileToSide={props.onOpenFileToSide}
      onPreviewFile={props.onPreviewFile}
    />
  );
}
