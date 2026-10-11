import React from 'react';
import { useApp } from '../context/AppContext';
import { useI18n } from '../i18n/context';
import {
  Printer,
  Radio,
  RefreshCw,
  Sun,
  Moon,
  LogOut,
  LayoutDashboard,
  FileText,
  Key,
  Eye,
  Activity,
  Cpu,
  Languages,
} from 'lucide-react';
import { TranslationKey } from '../i18n/en';

export type NavTab =
  | 'overview'
  | 'printers'
  | 'agents'
  | 'discovery'
  | 'jobs'
  | 'tokens'
  | 'preview'
  | 'ops';

interface HeaderProps {
  currentTab: NavTab;
  setTab: (tab: NavTab) => void;
}

export const Header: React.FC<HeaderProps> = ({ currentTab, setTab }) => {
  const {
    theme,
    toggleTheme,
    logout,
    autoRefresh,
    setAutoRefresh,
    sseConnected,
    refreshAll,
    loading,
    fleetStats,
  } = useApp();

  const { t, locale, toggleLocale } = useI18n();

  const navItems: { id: NavTab; labelKey: TranslationKey; icon: React.ReactNode }[] = [
    { id: 'overview', labelKey: 'nav_overview', icon: <LayoutDashboard size={15} /> },
    { id: 'printers', labelKey: 'nav_printers', icon: <Printer size={15} /> },
    { id: 'agents', labelKey: 'nav_agents', icon: <Cpu size={15} /> },
    { id: 'discovery', labelKey: 'nav_discovery', icon: <Radio size={15} /> },
    { id: 'jobs', labelKey: 'nav_jobs', icon: <FileText size={15} /> },
    { id: 'tokens', labelKey: 'nav_tokens', icon: <Key size={15} /> },
    { id: 'preview', labelKey: 'nav_preview', icon: <Eye size={15} /> },
    { id: 'ops', labelKey: 'nav_ops', icon: <Activity size={15} /> },
  ];

  return (
    <header className="topbar">
      <div className="brand" onClick={() => setTab('overview')} style={{ cursor: 'pointer' }}>
        <span
          className={`dot ${fleetStats.notUsable > 0 ? 'bad' : fleetStats.usable > 0 ? 'ok' : ''}`}
        />
        <span className="brand-text">{t('brand_title')}</span>
      </div>

      <nav className="nav-tabs">
        {navItems.map((item) => (
          <button
            key={item.id}
            className={`nav-tab ${currentTab === item.id ? 'active' : ''}`}
            onClick={() => setTab(item.id)}
          >
            {item.icon}
            <span>{t(item.labelKey)}</span>
          </button>
        ))}
      </nav>

      <div className="spacer" />

      <div className="topbar-actions">
        {/* SSE & Status Pill */}
        <span
          className={`pill ${sseConnected ? 'pill-live' : 'pill-poll'}`}
          title={sseConnected ? t('status_live_tip') : t('status_polling_tip')}
        >
          <span className="pulse-dot" />
          {sseConnected ? t('status_live') : t('status_polling')}
        </span>

        {/* Auto Refresh Toggle */}
        <button
          className={`btn btn-sm ${autoRefresh ? 'btn-active' : ''}`}
          onClick={() => setAutoRefresh(!autoRefresh)}
          title={autoRefresh ? `${t('auto_refresh')} (5s)` : `${t('auto_refresh')} (Paused)`}
        >
          <RefreshCw size={14} className={loading || autoRefresh ? 'spin-hover' : ''} />
          <span>{autoRefresh ? '5s' : t('none')}</span>
        </button>

        {/* Manual Refresh Button */}
        <button
          className="btn btn-sm btn-icon"
          onClick={() => refreshAll()}
          disabled={loading}
          title={t('refresh_now')}
        >
          <RefreshCw size={14} className={loading ? 'spinning' : ''} />
        </button>

        {/* Language Switcher */}
        <button
          className="btn btn-sm btn-lang"
          onClick={toggleLocale}
          title={t('lang_switch')}
        >
          <Languages size={14} />
          <span className="lang-text">{locale === 'zh-Hans' ? '简中' : 'EN'}</span>
        </button>

        {/* Theme Toggle */}
        <button
          className="btn btn-sm btn-icon"
          onClick={toggleTheme}
          title={theme === 'dark' ? t('theme_dark') : t('theme_light')}
        >
          {theme === 'dark' ? <Sun size={15} /> : <Moon size={15} />}
        </button>

        {/* Logout */}
        <button
          className="btn btn-sm btn-icon btn-danger-hover"
          onClick={logout}
          title={t('sign_out')}
        >
          <LogOut size={15} />
        </button>
      </div>

      {/* Mobile Bottom Navigation Bar (iOS Glass Island style) */}
      <nav className="mobile-bottom-bar ios-glass-island">
        {navItems.map((item) => (
          <button
            key={item.id}
            className={`mobile-tab-btn ${currentTab === item.id ? 'active' : ''}`}
            onClick={() => setTab(item.id)}
          >
            <span className="mobile-tab-icon">{item.icon}</span>
            <span className="mobile-tab-label">{t(item.labelKey)}</span>
          </button>
        ))}
      </nav>
    </header>
  );
};
