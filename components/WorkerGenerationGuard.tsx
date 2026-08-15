"use client";

import { useEffect, type ReactNode } from "react";
import { WORKER_GENERATION_CHANGED_EVENT } from "@/lib/worker-api.client";

export function WorkerGenerationGuard({ children }: { children: ReactNode }) {
  useEffect(() => {
    const reloadForWorker = () => window.location.reload();
    window.addEventListener(WORKER_GENERATION_CHANGED_EVENT, reloadForWorker);
    return () => window.removeEventListener(WORKER_GENERATION_CHANGED_EVENT, reloadForWorker);
  }, []);

  return children;
}
