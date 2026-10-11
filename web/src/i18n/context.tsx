import React, { createContext, useContext, useState, useEffect, useCallback } from 'react';
import { en, TranslationKey } from './en';
import { zh } from './zh';
import { PrinterState, Job } from '../types';

export type SupportedLocale = 'en' | 'zh-Hans';
export const LOCALE_KEY = 'cps.locale';

interface I18nContextType {
  locale: SupportedLocale;
  setLocale: (locale: SupportedLocale) => void;
  toggleLocale: () => void;
  t: (key: TranslationKey, params?: Record<string, string | number>) => string;
  tPrinterState: (state?: PrinterState | string) => string;
  tJobState: (state?: Job['state'] | string) => string;
}

const dictionaries: Record<SupportedLocale, Record<TranslationKey, string>> = {
  en: en,
  'zh-Hans': zh,
};

const I18nContext = createContext<I18nContextType | null>(null);

export const I18nProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const getInitialLocale = (): SupportedLocale => {
    const saved = localStorage.getItem(LOCALE_KEY);
    if (saved === 'en' || saved === 'zh-Hans') {
      return saved;
    }
    // Auto-detect browser preference
    if (typeof navigator !== 'undefined' && navigator.language?.toLowerCase().startsWith('zh')) {
      return 'zh-Hans';
    }
    return 'en';
  };

  const [locale, setLocaleState] = useState<SupportedLocale>(getInitialLocale);

  const setLocale = (l: SupportedLocale) => {
    setLocaleState(l);
    localStorage.setItem(LOCALE_KEY, l);
    document.documentElement.lang = l === 'zh-Hans' ? 'zh-CN' : 'en';
  };

  useEffect(() => {
    document.documentElement.lang = locale === 'zh-Hans' ? 'zh-CN' : 'en';
  }, [locale]);

  const toggleLocale = () => {
    setLocale(locale === 'en' ? 'zh-Hans' : 'en');
  };

  const t = useCallback(
    (key: TranslationKey, params?: Record<string, string | number>): string => {
      const dict = dictionaries[locale] || en;
      let text = dict[key] || en[key] || String(key);

      if (params) {
        for (const [k, v] of Object.entries(params)) {
          text = text.replace(new RegExp(`\\{${k}\\}`, 'g'), String(v));
        }
      }
      return text;
    },
    [locale]
  );

  const tPrinterState = useCallback(
    (state?: PrinterState | string): string => {
      if (!state) return t('state_unknown');
      const key = `state_${state}` as TranslationKey;
      return dictionaries[locale]?.[key] || dictionaries['en'][key] || state;
    },
    [locale, t]
  );

  const tJobState = useCallback(
    (state?: Job['state'] | string): string => {
      if (!state) return t('state_queued');
      const key = `state_${state}` as TranslationKey;
      return dictionaries[locale]?.[key] || dictionaries['en'][key] || state;
    },
    [locale, t]
  );

  return (
    <I18nContext.Provider
      value={{
        locale,
        setLocale,
        toggleLocale,
        t,
        tPrinterState,
        tJobState,
      }}
    >
      {children}
    </I18nContext.Provider>
  );
};

export const useI18n = () => {
  const ctx = useContext(I18nContext);
  if (!ctx) throw new Error('useI18n must be used within an I18nProvider');
  return ctx;
};
