import React from 'react';
import { useApp } from '../context/AppContext';
import { useI18n } from '../i18n/context';
import { Printer } from '../types';
import { NavTab } from '../components/Header';
import {
  Printer as PrinterIcon,
  CheckCircle2,
  AlertTriangle,
  Clock,
  ArrowRight,
  Radio,
} from 'lucide-react';

interface OverviewViewProps {
  setTab: (tab: NavTab) => void;
  onSelectPrinter: (printerId: string) => void;
}

export const OverviewView: React.FC<OverviewViewProps> = ({ setTab, onSelectPrinter }) => {
  const { printers, jobs, fleetStats, candidates, agents } = useApp();
  const { t, tJobState } = useI18n();

  const attentionPrinters = printers.filter((p) => !p.usable);
  const usablePrinters = printers.filter((p) => p.usable);
  const recentJobs = jobs.slice(0, 10);

  const operationalPct =
    fleetStats.total > 0 ? Math.round((fleetStats.usable / fleetStats.total) * 100) : 0;

  return (
    <div className="view-container">
      {/* Top Stat Cards */}
      <div className="stats-cards">
        <div className="card stat-card" onClick={() => setTab('printers')}>
          <div className="stat-header">
            <span className="stat-label">{t('stat_total_printers')}</span>
            <PrinterIcon size={16} className="text-muted" />
          </div>
          <div className="stat-value">{fleetStats.total}</div>
          <div className="stat-sub">{t('stat_active_agents', { count: agents.length })}</div>
        </div>

        <div className="card stat-card" onClick={() => setTab('printers')}>
          <div className="stat-header">
            <span className="stat-label">{t('stat_usable_fleet')}</span>
            <CheckCircle2 size={16} className="text-ok" />
          </div>
          <div
            className={`stat-value ${
              fleetStats.usable === fleetStats.total
                ? 'text-ok'
                : fleetStats.usable > 0
                ? 'text-warn'
                : 'text-bad'
            }`}
          >
            {fleetStats.usable} / {fleetStats.total}
          </div>
          <div className="stat-sub">
            {fleetStats.total > 0
              ? t('stat_operational_pct', { pct: operationalPct })
              : t('stat_none_registered')}
          </div>
        </div>

        <div className="card stat-card" onClick={() => setTab('printers')}>
          <div className="stat-header">
            <span className="stat-label">{t('stat_needs_attention')}</span>
            <AlertTriangle
              size={16}
              className={fleetStats.notUsable > 0 ? 'text-bad' : 'text-muted'}
            />
          </div>
          <div className={`stat-value ${fleetStats.notUsable > 0 ? 'text-bad' : 'text-ok'}`}>
            {fleetStats.notUsable}
          </div>
          <div className="stat-sub">
            {fleetStats.notUsable > 0 ? t('stat_attention_sub') : t('stat_all_healthy')}
          </div>
        </div>

        <div className="card stat-card" onClick={() => setTab('jobs')}>
          <div className="stat-header">
            <span className="stat-label">{t('stat_active_queue')}</span>
            <Clock size={16} className="text-muted" />
          </div>
          <div className="stat-value">{fleetStats.queuedTotal}</div>
          <div className="stat-sub">{t('stat_queue_sub')}</div>
        </div>

        <div className="card stat-card" onClick={() => setTab('discovery')}>
          <div className="stat-header">
            <span className="stat-label">{t('stat_candidates')}</span>
            <Radio size={16} className="text-accent" />
          </div>
          <div className="stat-value">{candidates.length}</div>
          <div className="stat-sub">{t('stat_candidates_sub')}</div>
        </div>
      </div>

      {/* Needs Attention Section (if any) */}
      {attentionPrinters.length > 0 && (
        <section className="section-block">
          <div className="section-header">
            <h2 className="section-title text-bad">
              <AlertTriangle size={16} /> {t('section_needs_attention', { count: attentionPrinters.length })}
            </h2>
          </div>
          <div className="grid-tiles">
            {attentionPrinters.map((p) => (
              <PrinterTile key={p.printer_id} printer={p} onClick={() => onSelectPrinter(p.printer_id)} />
            ))}
          </div>
        </section>
      )}

      {/* Usable Fleet Grid */}
      <section className="section-block">
        <div className="section-header">
          <h2 className="section-title">
            {t('section_operational_fleet', { count: usablePrinters.length })}
          </h2>
          <button className="link-action" onClick={() => setTab('printers')}>
            {t('view_all_printers')} <ArrowRight size={14} />
          </button>
        </div>
        {usablePrinters.length > 0 ? (
          <div className="grid-tiles">
            {usablePrinters.map((p) => (
              <PrinterTile key={p.printer_id} printer={p} onClick={() => onSelectPrinter(p.printer_id)} />
            ))}
          </div>
        ) : (
          <div className="empty-box">{t('no_operational_printers')}</div>
        )}
      </section>

      {/* Recent Jobs Table */}
      <section className="section-block">
        <div className="section-header">
          <h2 className="section-title">{t('section_recent_jobs')}</h2>
          <button className="link-action" onClick={() => setTab('jobs')}>
            {t('view_all_jobs')} <ArrowRight size={14} />
          </button>
        </div>
        <div className="table-responsive">
          <table className="data-table">
            <thead>
              <tr>
                <th>{t('th_job_id')}</th>
                <th>{t('th_printer_id')}</th>
                <th>{t('status')}</th>
                <th>{t('th_client')}</th>
                <th>{t('th_doc_ref')}</th>
                <th>{t('th_payload')}</th>
                <th>{t('th_submitted')}</th>
              </tr>
            </thead>
            <tbody>
              {recentJobs.length > 0 ? (
                recentJobs.map((j) => (
                  <tr key={j.job_id}>
                    <td className="font-mono text-sm">{j.job_id}</td>
                    <td className="font-mono text-sm">{j.printer_id}</td>
                    <td>
                      <span className={`state-badge state-${j.state}`}>{tJobState(j.state)}</span>
                      {j.attempts > 1 && (
                        <span className="badge-pill ml-1">{j.attempts} tries</span>
                      )}
                      {j.partial_write && (
                        <span className="badge-pill badge-danger ml-1">partial</span>
                      )}
                    </td>
                    <td className="text-muted">{j.client_id}</td>
                    <td className="font-mono text-sm">{j.biz_ref || j.external_id || '—'}</td>
                    <td className="text-muted">{j.payload_bytes} B</td>
                    <td className="text-muted">{formatAgo(j.queued_at)}</td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={7} className="text-center text-muted py-4">
                    {t('no_recent_jobs')}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
};

export const PrinterTile: React.FC<{ printer: Printer; onClick: () => void }> = ({
  printer: p,
  onClick,
}) => {
  const { t, tPrinterState } = useI18n();
  const st = p.status || { state: 'unknown' };
  return (
    <div className={`tile-card ${!p.usable ? 'tile-unusable' : ''}`} onClick={onClick}>
      <div className="tile-top">
        <div>
          <div className="flex items-center gap-1.5">
            <span className="tile-name">{p.name || p.printer_id}</span>
            {p.custom_label && (
              <span className="badge-pill badge-custom-label text-xs">
                {p.custom_label}
              </span>
            )}
          </div>
          <div className="tile-meta">
            {p.printer_id} · {p.site || '—'} · {p.model || p.driver}
          </div>
          {p.remark && <div className="text-xs text-muted mt-1">{p.remark}</div>}
        </div>
        <span className={`state-badge state-${st.state}`}>{tPrinterState(st.state)}</span>
      </div>

      <div className="tile-bottom">
        <span className={`badge-pill ${p.usable ? 'badge-ok' : 'badge-danger'}`}>
          {p.usable ? t('usable') : t('not_usable')}
        </span>
        {p.queue_depth > 0 && (
          <span className="badge-pill badge-info">{t('queue_prefix', { count: p.queue_depth })}</span>
        )}
        <span className="badge-pill text-muted">
          {t('seen_ago', { time: formatAgo(st.observed_at) })}
        </span>
        {st.stale && <span className="badge-pill badge-danger">{t('stale')}</span>}
      </div>
    </div>
  );
};

function formatAgo(isoString?: string): string {
  if (!isoString) return 'never';
  const sec = Math.max(0, Math.round((Date.now() - new Date(isoString).getTime()) / 1000));
  if (sec < 60) return `${sec}s ago`;
  const min = Math.round(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr}h ago`;
  return `${Math.round(hr / 24)}d ago`;
}
