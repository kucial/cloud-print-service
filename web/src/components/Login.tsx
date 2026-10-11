import React, { useState } from 'react';
import { useApp } from '../context/AppContext';
import { useI18n } from '../i18n/context';
import { api } from '../api';
import { useToast } from './Toast';
import { KeyRound, ExternalLink, Printer, Languages } from 'lucide-react';

export const Login: React.FC = () => {
  const [tokenInput, setTokenInput] = useState('');
  const [loading, setLoading] = useState(false);
  const { setToken } = useApp();
  const { t, locale, toggleLocale } = useI18n();
  const { toast } = useToast();

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = tokenInput.trim();
    if (!trimmed) {
      toast(t('login_token_required'), 'warning');
      return;
    }

    setLoading(true);
    try {
      localStorage.setItem('cps.token', trimmed);
      await api.getFleet();
      setToken(trimmed);
      toast(t('login_success'), 'success');
    } catch (err: any) {
      localStorage.removeItem('cps.token');
      toast(err.message || t('login_failed'), 'error');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="login-wrap">
      <div className="login-card">
        <div className="login-top-actions flex justify-end mb-2">
          <button
            type="button"
            className="btn btn-xs btn-secondary"
            onClick={toggleLocale}
            title={t('lang_switch')}
          >
            <Languages size={13} />
            <span>{locale === 'zh-Hans' ? 'English' : '简体中文'}</span>
          </button>
        </div>

        <div className="login-header">
          <div className="login-icon-badge">
            <Printer size={28} className="text-accent" />
          </div>
          <h1>{t('login_title')}</h1>
          <p className="login-subtitle">{t('login_subtitle')}</p>
        </div>

        <form onSubmit={handleSubmit}>
          <div className="form-group">
            <label htmlFor="token-input">{t('login_token_label')}</label>
            <div className="input-with-icon">
              <KeyRound size={16} className="input-icon" />
              <input
                id="token-input"
                type="password"
                value={tokenInput}
                onChange={(e) => setTokenInput(e.target.value)}
                placeholder={t('login_token_placeholder')}
                autoFocus
                autoComplete="off"
              />
            </div>
          </div>

          <button type="submit" className="btn btn-primary btn-block" disabled={loading}>
            {loading ? t('login_verifying') : t('login_submit')}
          </button>
        </form>

        <div className="login-footer">
          <p className="help-text">
            {t('login_help')}
            <br />
            <code>cpsctl token issue --client-id ops --role admin</code>
          </p>
          <div className="login-links">
            <a href="/readyz" target="_blank" rel="noreferrer" className="link-subtle">
              {t('login_health')} <ExternalLink size={12} />
            </a>
            <a href="/metrics" target="_blank" rel="noreferrer" className="link-subtle">
              {t('login_metrics')} <ExternalLink size={12} />
            </a>
          </div>
        </div>
      </div>
    </div>
  );
};
