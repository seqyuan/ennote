"use client";

import { useCallback, useEffect, useId, useRef, useState } from "react";
import { Button } from "./index";
import styles from "./confirm.module.css";

export interface ConfirmOptions {
  title: string;
  description?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  tone?: "default" | "danger";
}

interface ConfirmRequest {
  id: number;
  options: ConfirmOptions;
  resolve: (value: boolean) => void;
}

const queue: ConfirmRequest[] = [];
let nextId = 1;
let listener: (() => void) | null = null;

function subscribeConfirm(pull: () => void): () => void {
  listener = pull;
  return () => {
    if (listener === pull) listener = null;
  };
}

function takeConfirm(): ConfirmRequest | null {
  return queue.shift() ?? null;
}

/**
 * Promise-based replacement for window.confirm. The host is mounted once by
 * the app shell; if it is absent (unit tests, a non-app surface), fall back to
 * the native prompt so a call can never hang.
 */
export function confirmAction(options: ConfirmOptions): Promise<boolean> {
  if (typeof window === "undefined") return Promise.resolve(false);
  if (!listener) {
    const message = [options.title, options.description].filter(Boolean).join("\n\n");
    return Promise.resolve(window.confirm(message));
  }
  return new Promise<boolean>((resolve) => {
    queue.push({ id: nextId++, options, resolve });
    listener?.();
  });
}

export function ConfirmHost() {
  const [request, setRequest] = useState<ConfirmRequest | null>(null);
  const current = useRef<ConfirmRequest | null>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const previousFocus = useRef<HTMLElement | null>(null);
  const titleId = useId();
  const descriptionId = useId();

  const showNext = useCallback(() => {
    if (current.current !== null) return;
    const next = takeConfirm();
    if (next === null) return;
    current.current = next;
    setRequest(next);
  }, []);

  useEffect(() => {
    const unsubscribe = subscribeConfirm(showNext);
    // Drain a request queued before the host mounted; defer the state write so
    // the effect only subscribes and does not cascade a render synchronously.
    queueMicrotask(showNext);
    return unsubscribe;
  }, [showNext]);

  const settle = useCallback((value: boolean) => {
    const active = current.current;
    if (active === null) return;
    current.current = null;
    active.resolve(value);
    setRequest(null);
    // Keep a queued request moving without waiting for another render tick.
    const next = takeConfirm();
    if (next !== null) {
      current.current = next;
      setRequest(next);
    }
  }, []);

  useEffect(() => {
    if (request === null) return;
    previousFocus.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const frame = window.requestAnimationFrame(() => cancelRef.current?.focus());
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        settle(false);
        return;
      }
      if (event.key !== "Tab" || dialogRef.current === null) return;
      const focusable = Array.from(dialogRef.current.querySelectorAll<HTMLElement>(
        'button:not(:disabled), [href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])',
      )).filter((element) => element.getClientRects().length > 0);
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => {
      window.cancelAnimationFrame(frame);
      document.removeEventListener("keydown", onKeyDown);
      previousFocus.current?.focus();
    };
  }, [request, settle]);

  if (request === null) return null;
  const { options } = request;

  return (
    <div
      className={styles.overlay}
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) settle(false);
      }}
    >
      <div
        ref={dialogRef}
        className={styles.dialog}
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={options.description ? descriptionId : undefined}
      >
        <h2 id={titleId} className={styles.title}>{options.title}</h2>
        {options.description && <p id={descriptionId} className={styles.description}>{options.description}</p>}
        <div className={styles.actions}>
          <Button ref={cancelRef} variant="secondary" onClick={() => settle(false)}>
            {options.cancelLabel ?? "Cancel"}
          </Button>
          <Button
            variant={options.tone === "danger" ? "danger" : "primary"}
            onClick={() => settle(true)}
          >
            {options.confirmLabel ?? "Confirm"}
          </Button>
        </div>
      </div>
    </div>
  );
}
