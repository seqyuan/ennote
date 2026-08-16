"use client";

import { useCallback, useState } from "react";
import type { TextAttachment } from "@/components/Composer";
import { errorMessage } from "@/lib/provider-errors";
import { apiFetch } from "@/lib/worker-api.client";
import type { components } from "@/lib/worker-api.gen";

type ImageArtifact = components["schemas"]["ImageArtifact"];

function genId(): string {
  if (typeof crypto !== "undefined" && crypto.randomUUID) return crypto.randomUUID();
  return Math.random().toString(36).slice(2) + Date.now().toString(36);
}

const TEXT_ATTACHMENT_EXTENSIONS = new Set([
  "txt", "md", "mdx", "json", "yaml", "yml", "toml", "csv", "tsv", "log", "js", "jsx", "ts", "tsx",
  "py", "r", "go", "rs", "java", "c", "cpp", "h", "hpp", "css", "html", "xml", "sh", "bash", "sql",
]);

function isSupportedTextAttachment(file: File): boolean {
  if (file.type.startsWith("text/")) return true;
  const extension = file.name.toLowerCase().split(".").pop() ?? "";
  return TEXT_ATTACHMENT_EXTENSIONS.has(extension) || ["dockerfile", "makefile"].includes(file.name.toLowerCase());
}

type AttachmentRuntime = {
  setStatus: (status: string) => void;
  setError: (error: string | null) => void;
};

/**
 * Composer attachments: one pending image + up to three text attachments.
 * Owns upload/attach/remove plus the take/restore lifecycle used by sendTurn
 * (snapshot on send, restore on failure) and the declarative reset.
 */
export function useAttachments(deps: {
  selectedProject: string | null;
  selectedSession: string | null;
  runtime: AttachmentRuntime;
}) {
  const { selectedProject, selectedSession, runtime } = deps;
  const [pendingImage, setPendingImage] = useState<ImageArtifact | null>(null);
  const [textAttachments, setTextAttachments] = useState<TextAttachment[]>([]);

  const uploadImage = useCallback(async (file: File) => {
    if (!selectedProject || !selectedSession) return;
    const data = new FormData();
    data.set("sessionId", selectedSession);
    data.set("file", file);
    try {
      runtime.setStatus("uploading image...");
      const artifact = await apiFetch<ImageArtifact>(`/v1/projects/${encodeURIComponent(selectedProject)}/attachments/images`, {
        method: "POST", body: data,
      });
      setPendingImage(artifact);
      runtime.setError(null);
    } catch (reason) {
      runtime.setError(errorMessage(reason, "Failed to attach the image"));
    } finally {
      runtime.setStatus("");
    }
  }, [selectedProject, selectedSession, runtime]);

  const attachFiles = useCallback(async (files: File[]) => {
    if (!selectedSession) return;
    const images = files.filter((file) => file.type.startsWith("image/"));
    const documents = files.filter((file) => !file.type.startsWith("image/"));
    if (images[0]) await uploadImage(images[0]);
    if (images.length > 1) runtime.setError("Only one image can be attached to a turn.");

    const accepted: TextAttachment[] = [];
    for (const file of documents) {
      if (!isSupportedTextAttachment(file)) {
        runtime.setError(`${file.name} is not a supported text attachment.`);
        continue;
      }
      if (file.size > 1 << 20) {
        runtime.setError(`${file.name} exceeds the 1 MiB text attachment limit.`);
        continue;
      }
      accepted.push({ id: genId(), name: file.name, size: file.size, text: await file.text() });
    }
    if (accepted.length) {
      setTextAttachments((current) => [...current, ...accepted].slice(0, 3));
      if (textAttachments.length + accepted.length > 3) runtime.setError("A turn can include at most three text files.");
    }
  }, [selectedSession, runtime, textAttachments.length, uploadImage]);

  const removeTextAttachment = useCallback((id: string) => {
    setTextAttachments((current) => current.filter((item) => item.id !== id));
  }, []);

  const clearPendingImage = useCallback(() => setPendingImage(null), []);
  const clearAttachments = useCallback(() => {
    setPendingImage(null);
    setTextAttachments([]);
  }, []);
  const restoreAttachments = useCallback((image: ImageArtifact | null, attachments: TextAttachment[]) => {
    setPendingImage(image);
    setTextAttachments(attachments);
  }, []);

  return {
    pendingImage, textAttachments,
    uploadImage, attachFiles, removeTextAttachment,
    clearPendingImage, clearAttachments, restoreAttachments,
  };
}
