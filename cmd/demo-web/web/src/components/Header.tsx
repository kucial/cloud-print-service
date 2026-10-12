import React, { useState, useEffect } from 'react';
import { useI18n } from '../i18n/context';
import { AppConfig } from '../types';
import { Package, RefreshCw, Languages, Sun, Moon } from 'lucide-react';

const THEME_KEY = 'demo_web.theme';

interface HeaderProps {
  config: AppConfig | null;
  loading: boolean;
  connected: boolean;
  autoRefresh: boolean;
  setAutoRefresh: (val: boolean) => void;
  onRefresh: () => void;
}

export const Header: React.FC<HeaderProps> = ({
  config,
  loading,
  connected,
  autoRefresh,
  setAutoRefresh,
  onRefresh,
}) => {
  const { t, locale, toggleLocale } = useI18n();
  const [theme, setTheme] = useState<'dark' | 'light'>('dark');

  useEffect(() => {
    const saved = (localStorage.getItem(THEME_KEY) as 'dark' | 'light') || 'dark';
    setTheme(saved);
    document.documentElement.dataset.theme = saved;
  }, []);

  const toggleTheme = () => {
    const next = theme === 'dark' ? 'light' : 'dark';
    setTheme(next);
    localStorage.setItem(THEME_KEY, next);
    document.documentElement.dataset.theme = next;
  };

  return (
    <header className="topbar">
      <div className="brand">
        <div className="brand-icon-wrap">
          <Package size={22} className="text-accent" />
        </div>
        <div>
          <div className="brand-title">{t('brand_title')}</div>
          <div className="brand-sub">{t('brand_sub')}</div>
        </div>
      </div>

      <div className="spacer" />

      <div className="header-actions">
        {/* Connection status pill */}
        <span
          className={`pill ${connected ? 'pill-live' : 'pill-poll'}`}
          title={connected ? config?.cps : t('connecting')}
        >
          <span className="pulse-dot" />
          <span>{connected ? `${t('connected')} · ${config?.cps || 'CPS'}` : t('connecting')}</span>
        </span>

        {/* Auto Refresh Toggle */}
        <button
          className={`btn btn-sm ${autoRefresh ? 'btn-active' : ''}`}
          onClick={() => setAutoRefresh(!autoRefresh)}
          title={autoRefresh ? 'Auto-refresh active (5s)' : 'Auto-refresh paused'}
        >
          <RefreshCw size={13} className={loading || autoRefresh ? 'spin-hover' : ''} />
          <span>{autoRefresh ? '5s' : 'Off'}</span>
        </button>

        {/* Refresh Now */}
        <button
          className="btn btn-sm btn-icon"
          onClick={onRefresh}
          disabled={loading}
          title={t('refresh')}
        >
          <RefreshCw size={14} className={loading ? 'spinning' : ''} />
        </button>

        {/* Language Switch */}
        <button
          className="btn btn-sm btn-lang"
          onClick={toggleLocale}
          title={t('lang_switch')}
        >
          <Languages size={14} />
          <span>{locale === 'zh-Hans' ? '简中' : 'EN'}</span>
        </button>

        {/* Theme Switch */}
        <button
          className="btn btn-sm btn-icon"
          onClick={toggleTheme}
          title={theme === 'dark' ? t('theme_light') : t('theme_dark')}
        >
          {theme === 'dark' ? <Sun size={15} /> : <Moon size={15} />}
        </button>
      </div>
    </header>
  );
};
