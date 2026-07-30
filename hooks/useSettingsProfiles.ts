"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { ModelProfile, PolicyProfile, ProviderProfile } from "@/components/settings/types";
import { apiFetch } from "@/lib/worker-api.client";

export function useSettingsProfiles() {
  const [providers, setProviders] = useState<ProviderProfile[]>([]);
  const [models, setModels] = useState<ModelProfile[]>([]);
  const [policies, setPolicies] = useState<PolicyProfile[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const generation = useRef(0);
  const controller = useRef<AbortController | null>(null);

  const refresh = useCallback(async () => {
    controller.current?.abort();
    const activeController = new AbortController();
    controller.current = activeController;
    const version = ++generation.current;
    try {
      const [nextProviders, nextModels, nextPolicies] = await Promise.all([
        apiFetch<ProviderProfile[]>("/v1/provider-profiles", { signal: activeController.signal }),
        apiFetch<ModelProfile[]>("/v1/model-profiles", { signal: activeController.signal }),
        apiFetch<PolicyProfile[]>("/v1/policy-profiles", { signal: activeController.signal }),
      ]);
      if (activeController.signal.aborted || generation.current !== version) return;
      setProviders(nextProviders);
      setModels(nextModels);
      setPolicies(nextPolicies);
      setError(null);
    } catch (reason) {
      if (!activeController.signal.aborted && generation.current === version) {
        setError((reason as Error).message);
      }
    } finally {
      if (!activeController.signal.aborted && generation.current === version) setLoading(false);
    }
  }, []);

  useEffect(() => {
    const timer = window.setTimeout(() => void refresh(), 0);
    return () => {
      window.clearTimeout(timer);
      controller.current?.abort();
    };
  }, [refresh]);

  return { providers, models, policies, error, loading, setError, refresh };
}
