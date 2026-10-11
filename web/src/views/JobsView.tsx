import React, { useState } from 'react';
import { useApp } from '../context/AppContext';
import { useI18n } from '../i18n/context';
import { Job, JobAttempt } from '../types';
import { api } from '../api';
import { useToast } from '../components/Toast';
import {
  Search,
  Ban,
  RotateCw,
  X,
  History,
} from 'lucide-react';

export const JobsView: React.FC = () => {
  const { jobs, printers, refreshJobs } = useApp();
  const { t, tJobState } = useI18n();
  const { toast } = useToast();

  const [stateFilter, setStateFilter] = useState<string>('');
  const [printerFilter, setPrinterFilter] = useState<string>('');
  const [search, setSearch] = useState<string>('');
  const [selectedJob, setSelectedJob] = useState<Job | null>(null);
  const [attempts, setAttempts] = useState<JobAttempt[]>([]);
  const [loadingAttempts, setLoadingAttempts] = useState(false);
  const [cancellingId, setCancellingId] = useState<string | null>(null);

  const filteredJobs = jobs.filter((j) => {
    const matchesState = !stateFilter || j.state === stateFilter;
    const matchesPrinter = !printerFilter || j.printer_id === printerFilter;
    const q = search.toLowerCase();
    const matchesSearch =
      !search ||
      j.job_id.toLowerCase().includes(q) ||
      j.printer_id.toLowerCase().includes(q) ||
      (j.external_id && j.external_id.toLowerCase().includes(q)) ||
      (j.biz_ref && j.biz_ref.toLowerCase().includes(q));

    return matchesState && matchesPrinter && matchesSearch;
  });

  const handleCancel = async (jobId: string) => {
    setCancellingId(jobId);
    try {
      await api.cancelJob(jobId);
      await refreshJobs();
      toast(t('job_cancelled', { id: jobId }), 'success');
    } catch (e: any) {
      toast(e.message || 'Failed to cancel job', 'error');
    } finally {
      setCancellingId(null);
    }
  };

  const handleViewAttempts = async (j: Job) => {
    setSelectedJob(j);
    setLoadingAttempts(true);
    try {
      const res = await api.getJobAttempts(j.job_id);
      setAttempts(res.items || []);
    } catch (e: any) {
      toast(e.message || 'Failed to load attempts history', 'error');
    } finally {
      setLoadingAttempts(false);
    }
  };

  return (
    <div className="view-container">
      <div className="view-header">
        <div>
          <h1 className="view-title">{t('jobs_title')}</h1>
          <p className="view-subtitle">{t('jobs_subtitle')}</p>
        </div>

        <div className="header-actions">
          <button className="btn btn-secondary" onClick={() => refreshJobs()}>
            <RotateCw size={14} />
            <span>{t('refresh')}</span>
          </button>
        </div>
      </div>

      {/* Filter Bar */}
      <div className="filter-bar">
        <div className="search-input-wrap">
          <Search size={15} className="search-icon" />
          <input
            type="text"
            placeholder={t('search_jobs')}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          {search && (
            <button className="clear-btn" onClick={() => setSearch('')}>
              ×
            </button>
          )}
        </div>

        <select value={stateFilter} onChange={(e) => setStateFilter(e.target.value)}>
          <option value="">{t('all_states')}</option>
          <option value="queued">{t('state_queued')}</option>
          <option value="sending">{t('state_sending')}</option>
          <option value="printed">{t('state_printed')}</option>
          <option value="sent_unknown">{t('state_sent_unknown')}</option>
          <option value="failed">{t('state_failed')}</option>
          <option value="cancelled">{t('state_cancelled')}</option>
        </select>

        <select value={printerFilter} onChange={(e) => setPrinterFilter(e.target.value)}>
          <option value="">{t('all_printers')}</option>
          {printers.map((p) => (
            <option key={p.printer_id} value={p.printer_id}>
              {p.name || p.printer_id}
            </option>
          ))}
        </select>
      </div>

      {/* Jobs Table */}
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
              <th>{t('th_duration')}</th>
              <th>{t('actions')}</th>
            </tr>
          </thead>
          <tbody>
            {filteredJobs.length > 0 ? (
              filteredJobs.map((j) => {
                const canCancel = j.state === 'queued' || j.state === 'sending';
                return (
                  <tr key={j.job_id}>
                    <td className="font-mono font-medium">{j.job_id}</td>
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
                    <td className="text-muted text-sm">{j.client_id}</td>
                    <td className="font-mono text-sm">{j.biz_ref || j.external_id || '—'}</td>
                    <td className="text-muted text-sm">{j.payload_bytes} B</td>
                    <td className="text-muted text-sm">{formatAgo(j.queued_at)}</td>
                    <td className="text-muted text-sm font-mono">{formatDuration(j)}</td>
                    <td>
                      <div className="table-btn-group">
                        <button
                          className="btn btn-xs btn-secondary"
                          onClick={() => handleViewAttempts(j)}
                          title={t('btn_attempts')}
                        >
                          <History size={11} /> {t('btn_attempts')}
                        </button>
                        {canCancel && (
                          <button
                            className="btn btn-xs btn-danger"
                            onClick={() => handleCancel(j.job_id)}
                            disabled={cancellingId === j.job_id}
                            title={t('btn_cancel')}
                          >
                            <Ban size={11} /> {t('btn_cancel')}
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                );
              })
            ) : (
              <tr>
                <td colSpan={9} className="text-center text-muted py-6">
                  {t('no_matching_jobs')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {/* Attempts Modal */}
      {selectedJob && (
        <JobAttemptsModal
          job={selectedJob}
          attempts={attempts}
          loading={loadingAttempts}
          onClose={() => setSelectedJob(null)}
        />
      )}
    </div>
  );
};

interface JobAttemptsModalProps {
  job: Job;
  attempts: JobAttempt[];
  loading: boolean;
  onClose: () => void;
}

const JobAttemptsModal: React.FC<JobAttemptsModalProps> = ({
  job: j,
  attempts,
  loading,
  onClose,
}) => {
  const { t, tJobState } = useI18n();

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-card modal-lg" onClick={(e) => e.stopPropagation()}>
        <div className="modal-header">
          <div className="modal-title-wrap">
            <History size={18} className="text-accent" />
            <h2>{t('attempts_title', { id: j.job_id })}</h2>
          </div>
          <button className="modal-close-btn" onClick={onClose}>
            <X size={16} />
          </button>
        </div>

        <div className="modal-body">
          <div className="detail-grid mb-4">
            <div className="detail-item">
              <span className="detail-label">{t('th_printer_id')}</span>
              <span className="detail-val font-mono">{j.printer_id}</span>
            </div>
            <div className="detail-item">
              <span className="detail-label">{t('status')}</span>
              <span className="detail-val">
                <span className={`state-badge state-${j.state}`}>{tJobState(j.state)}</span>
              </span>
            </div>
            <div className="detail-item">
              <span className="detail-label">{t('attempts_external_id')}</span>
              <span className="detail-val font-mono">{j.external_id}</span>
            </div>
            <div className="detail-item">
              <span className="detail-label">{t('attempts_made')}</span>
              <span className="detail-val font-mono">{j.attempts}</span>
            </div>
            {j.last_error && (
              <div className="detail-item col-span-2">
                <span className="detail-label text-bad">{t('attempts_last_error')}</span>
                <span className="detail-val font-mono text-bad text-xs code-box">
                  {j.last_error}
                </span>
              </div>
            )}
          </div>

          <h3>{t('attempts_log_title')}</h3>
          {loading ? (
            <div className="p-4 text-center text-muted">{t('loading')}</div>
          ) : attempts.length > 0 ? (
            <div className="table-responsive">
              <table className="data-table text-sm">
                <thead>
                  <tr>
                    <th>{t('th_attempt_no')}</th>
                    <th>{t('th_started')}</th>
                    <th>{t('th_duration')}</th>
                    <th>{t('th_result')}</th>
                    <th>{t('th_bytes_sent')}</th>
                    <th>{t('th_error_detail')}</th>
                  </tr>
                </thead>
                <tbody>
                  {attempts.map((a) => (
                    <tr key={a.id}>
                      <td className="font-mono font-medium">{a.attempt_no}</td>
                      <td className="text-muted font-mono text-xs">
                        {new Date(a.started_at).toLocaleTimeString()}
                      </td>
                      <td className="text-muted font-mono text-xs">
                        {a.finished_at
                          ? `${
                              new Date(a.finished_at).getTime() -
                              new Date(a.started_at).getTime()
                            }ms`
                          : t('in_flight')}
                      </td>
                      <td>
                        <span
                          className={`badge-pill ${
                            a.result === 'ok'
                              ? 'badge-ok'
                              : a.result === 'partial_write'
                              ? 'badge-danger'
                              : 'badge-warn'
                          }`}
                        >
                          {a.result}
                        </span>
                      </td>
                      <td className="font-mono text-xs">{a.bytes_sent} B</td>
                      <td className="font-mono text-xs text-bad">{a.error || '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <div className="empty-box">{t('no_attempts')}</div>
          )}
        </div>

        <div className="modal-footer">
          <button className="btn btn-secondary" onClick={onClose}>
            {t('close')}
          </button>
        </div>
      </div>
    </div>
  );
};

function formatDuration(j: Job): string {
  if (!j.sent_at) return '—';
  const start = new Date(j.queued_at).getTime();
  const end = j.finished_at ? new Date(j.finished_at).getTime() : Date.now();
  const diff = end - start;
  if (diff < 1000) return `${diff}ms`;
  return `${(diff / 1000).toFixed(1)}s`;
}

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
