import React, { useState, useEffect } from 'react';
import { api } from '../api';
import { useI18n } from '../i18n/context';
import { useToast } from '../components/Toast';
import { Activity, Search, RefreshCw, ExternalLink, CheckCircle2, XCircle } from 'lucide-react';

export const OpsView: React.FC = () => {
  const { t } = useI18n();
  const { toast } = useToast();
  const [metricsText, setMetricsText] = useState<string>('');
  const [metricFilter, setMetricFilter] = useState<string>('cps_');
  const [healthStatus, setHealthStatus] = useState<{ healthz: boolean; readyz: boolean; printers?: number } | null>(null);
  const [loading, setLoading] = useState(false);

  const fetchOpsData = async () => {
    setLoading(true);
    try {
      const [metrics, healthz, readyz] = await Promise.allSettled([
        api.getMetrics(),
        api.getHealthz(),
        api.getReadyz(),
      ]);

      if (metrics.status === 'fulfilled') {
        setMetricsText(metrics.value);
      }
      setHealthStatus({
        healthz: healthz.status === 'fulfilled' && (healthz.value as any)?.status === 'ok',
        readyz: readyz.status === 'fulfilled' && (readyz.value as any)?.status === 'ready',
        printers: readyz.status === 'fulfilled' ? (readyz.value as any)?.printers : undefined,
      });
    } catch {
      toast('Failed to refresh ops status', 'error');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchOpsData();
  }, []);

  const filteredMetrics = metricsText
    .split('\n')
    .filter((l) => (metricFilter ? l.toLowerCase().includes(metricFilter.toLowerCase()) : true))
    .slice(0, 150)
    .join('\n');

  return (
    <div className="view-container">
      <div className="view-header">
        <div>
          <h1 className="view-title">{t('ops_title')}</h1>
          <p className="view-subtitle">{t('ops_subtitle')}</p>
        </div>

        <div className="header-actions">
          <button className="btn btn-secondary" onClick={fetchOpsData} disabled={loading}>
            <RefreshCw size={14} className={loading ? 'spinning' : ''} />
            <span>{t('refresh')}</span>
          </button>
        </div>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-3 gap-6 mb-6">
        {/* Health Probe Card */}
        <div className="card">
          <h2 className="card-title mb-3">{t('card_service_health')}</h2>
          <div className="space-y-3 text-sm">
            <div className="flex items-center justify-between py-1 border-b border-line">
              <span>{t('health_liveness')}</span>
              {healthStatus?.healthz ? (
                <span className="badge-pill badge-ok">
                  <CheckCircle2 size={12} /> {t('health_ok')}
                </span>
              ) : (
                <span className="badge-pill badge-danger">
                  <XCircle size={12} /> {t('health_down')}
                </span>
              )}
            </div>

            <div className="flex items-center justify-between py-1 border-b border-line">
              <span>{t('health_readiness')}</span>
              {healthStatus?.readyz ? (
                <span className="badge-pill badge-ok">
                  <CheckCircle2 size={12} /> {t('health_ready')}
                </span>
              ) : (
                <span className="badge-pill badge-danger">
                  <XCircle size={12} /> {t('health_not_ready')}
                </span>
              )}
            </div>

            <div className="flex items-center justify-between py-1">
              <span className="text-muted">{t('health_registered_drivers')}</span>
              <span className="font-mono">{healthStatus?.printers ?? '—'}</span>
            </div>
          </div>
        </div>

        {/* Quick Links Card */}
        <div className="card">
          <h2 className="card-title mb-3">{t('card_diagnostics')}</h2>
          <div className="space-y-2 text-sm">
            <div>
              <a href="/readyz" target="_blank" rel="noreferrer" className="link-action">
                /readyz <ExternalLink size={12} />
              </a>
              <p className="text-xs text-muted mt-0.5">{t('diag_readyz_desc')}</p>
            </div>
            <div>
              <a href="/metrics" target="_blank" rel="noreferrer" className="link-action">
                /metrics <ExternalLink size={12} />
              </a>
              <p className="text-xs text-muted mt-0.5">{t('diag_metrics_desc')}</p>
            </div>
            <div>
              <a href="/api/v1/events" target="_blank" rel="noreferrer" className="link-action">
                /api/v1/events <ExternalLink size={12} />
              </a>
              <p className="text-xs text-muted mt-0.5">{t('diag_events_desc')}</p>
            </div>
          </div>
        </div>

        {/* Integration Tip Card */}
        <div className="card">
          <h2 className="card-title mb-2">{t('card_integration_tip')}</h2>
          <p className="text-xs text-muted leading-relaxed">
            {t('integration_tip_body')}
          </p>
        </div>
      </div>

      {/* Metrics Explorer */}
      <div className="card">
        <div className="flex items-center justify-between mb-4">
          <div className="flex items-center gap-2">
            <Activity size={16} className="text-accent" />
            <h2 className="card-title">{t('metrics_explorer_title')}</h2>
          </div>
          <div className="search-input-wrap w-64">
            <Search size={14} className="search-icon" />
            <input
              type="text"
              placeholder={t('filter_metrics_placeholder')}
              value={metricFilter}
              onChange={(e) => setMetricFilter(e.target.value)}
            />
          </div>
        </div>

        <pre className="metrics-box">{filteredMetrics || t('no_matching_metrics')}</pre>
      </div>
    </div>
  );
};
