"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { applyLocaleAttribute, readLocale, writeLocale, type Locale } from "@/lib/locale";
import { translate } from "@/lib/messages";

interface LocaleContextValue {
  locale: Locale;
  setLocale: (locale: Locale) => void;
}

const LocaleContext = createContext<LocaleContextValue>({ locale: "en", setLocale: () => {} });

export function LocaleProvider({ children }: { children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(() => readLocale());

  const setLocale = useCallback((next: Locale) => {
    setLocaleState(next);
    writeLocale(next);
    applyLocaleAttribute(next);
  }, []);

  // Reflect the persisted locale onto <html lang> on mount and on change.
  useEffect(() => {
    applyLocaleAttribute(locale);
  }, [locale]);

  const value = useMemo(() => ({ locale, setLocale }), [locale, setLocale]);
  return <LocaleContext.Provider value={value}>{children}</LocaleContext.Provider>;
}

export function useLocale(): LocaleContextValue {
  return useContext(LocaleContext);
}

/** Convenience translator bound to the active locale. */
export function useT(): (key: string) => string {
  const { locale } = useLocale();
  return (key: string) => translate(locale, key);
}
