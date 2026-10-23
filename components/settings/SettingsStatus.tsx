"use client";

import { CircleAlert, Check, Loader2 } from "lucide-react";
import {
  createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode,
} from "react";
import { useT } from "@/components/LocaleProvider";

type State = "idle" | "saving" | "saved" | "error";

interface SettingsStatus {
  state: State;
  message?: string;
}

interface SettingsStatusApi {
  status: SettingsStatus;
  markSaving: () => void;
  markSaved: () => void;
  markError: (message?: string) => void;
}

const IDLE: SettingsStatus = { state: "idle" };
const FALLBACK: SettingsStatusApi = {
  status: IDLE,
  markSaving: () => {},
  markSaved: () => {},
  markError: () => {},
};

const SettingsStatusContext = createContext<SettingsStatusApi>(FALLBACK);

/**
 * One save-status source for the settings dialog. Panels report a mutation as
 * saving, then saved or failed; the header shows a single live badge instead
 * of leaving each panel to invent its own "Saved" copy.
 */
export function SettingsStatusProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<SettingsStatus>(IDLE);
  const timer = useRef<number | undefined>(undefined);

  const clearTimer = useCallback(() => {
    if (timer.current === undefined) return;
    window.clearTimeout(timer.current);
    timer.current = undefined;
  }, []);

  const markSaving = useCallback(() => {
    clearTimer();
    setStatus({ state: "saving" });
  }, [clearTimer]);

  const markSaved = useCallback(() => {
    clearTimer();
    setStatus({ state: "saved" });
    timer.current = window.setTimeout(() => {
      timer.current = undefined;
      setStatus(IDLE);
    }, 2400);
  }, [clearTimer]);

  const markError = useCallback((message?: string) => {
    clearTimer();
    setStatus({ state: "error", message });
  }, [clearTimer]);

  useEffect(() => clearTimer, [clearTimer]);

  const value = useMemo<SettingsStatusApi>(
    () => ({ status, markSaving, markSaved, markError }),
    [status, markSaving, markSaved, markError],
  );

  return <SettingsStatusContext.Provider value={value}>{children}</SettingsStatusContext.Provider>;
}

export function useSettingsStatus(): SettingsStatusApi {
  return useContext(SettingsStatusContext);
}

export function SettingsStatusBadge() {
  const { status } = useSettingsStatus();
  const t = useT();
  if (status.state === "idle") return null;

  const label = status.state === "saving"
    ? t("settings.status.saving")
    : status.state === "saved"
      ? t("settings.status.saved")
      : t("settings.status.failed");
  const icon = status.state === "saving"
    ? <Loader2 size={13} className="spin" aria-hidden="true" />
    : status.state === "saved"
      ? <Check size={13} aria-hidden="true" />
      : <CircleAlert size={13} aria-hidden="true" />;

  return (
    <span
      className="settings-save-status"
      data-state={status.state}
      role="status"
      aria-live="polite"
      title={status.message}
    >
      {icon}
      <span>{label}</span>
    </span>
  );
}
