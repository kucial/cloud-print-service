import React, { useState } from 'react';
import { useApp } from '../context/AppContext';
import { useI18n } from '../i18n/context';
import { Printer } from '../types';
import { api } from '../api';
import { useToast } from '../components/Toast';
import {
  Printer as PrinterIcon,
  Search,
  CheckCircle2,
  XCircle,
  Play,
  RotateCw,
  X,
  Tag,
  FileEdit,
  Save,
} from 'lucide-react';

interface PrintersViewProps {
  selectedPrinterId: string | null;
  onSelectPrinter: (id: string | null) => void;
}

export const PrintersView: React.FC<PrintersViewProps> = ({
  selectedPrinterId,
  onSelectPrinter,
}) => {
  const { printers, refreshPrinters } = useApp();
  const { t, tPrinterState } = useI18n();
  const { toast } = useToast();

  const [search, setSearch] = useState('');
  const [siteFilter, setSiteFilter] = useState('');
  const [usableFilter, setUsableFilter] = useState<'all' | 'usable' | 'unusable'>('all');
  const [probing, setProbing] = useState(false);

  const selectedPrinter = printers.find((p) => p.printer_id === selectedPrinterId);
  const sites = Array.from(new Set(printers.map((p) => p.site).filter(Boolean)));

  const filteredPrinters = printers.filter((p) => {
    const q = search.toLowerCase();
    const matchesSearch =
      p.printer_id.toLowerCase().includes(q) ||
      (p.name && p.name.toLowerCase().includes(q)) ||
      (p.model && p.model.toLowerCase().includes(q)) ||
      (p.custom_label && p.custom_label.toLowerCase().includes(q)) ||
      (p.remark && p.remark.toLowerCase().includes(q));

    const matchesSite = !siteFilter || p.site === siteFilter;
    const matchesUsable =
      usableFilter === 'all' ||
      (usableFilter === 'usable' && p.usable) ||
      (usableFilter === 'unusable' && !p.usable);

    return matchesSearch && matchesSite && matchesUsable;
  });

  const handleProbeAll = async () => {
    setProbing(true);
    try {
      await api.probePrinters();
      await refreshPrinters();
      toast(t('probed_all_success'), 'success');
    } catch (e: any) {
      toast(e.message || 'Probe failed', 'error');
    } finally {
      setProbing(false);
    }
  };

  const handleTestPrint = async (printerId: string) => {
    try {
      const res = await api.testPrint(printerId);
      toast(t('test_label_queued', { id: res.job_id }), 'success');
    } catch (e: any) {
      toast(e.message || 'Failed to dispatch test print', 'error');
    }
  };

  return (
    <div className="view-container">
      {/* Header and Controls */}
      <div className="view-header">
        <div>
          <h1 className="view-title">{t('printers_title')}</h1>
          <p className="view-subtitle">{t('printers_subtitle')}</p>
        </div>

        <div className="header-actions">
          <button
            className="btn btn-secondary"
            onClick={handleProbeAll}
            disabled={probing}
          >
            <RotateCw size={14} className={probing ? 'spinning' : ''} />
            <span>{probing ? t('probing') : t('probe_all')}</span>
          </button>
        </div>
      </div>

      {/* Filter Bar */}
      <div className="filter-bar">
        <div className="search-input-wrap">
          <Search size={15} className="search-icon" />
          <input
            type="text"
            placeholder={t('search_printers')}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          {search && (
            <button className="clear-btn" onClick={() => setSearch('')}>
              ×
            </button>
          )}
        </div>

        {sites.length > 0 && (
          <select value={siteFilter} onChange={(e) => setSiteFilter(e.target.value)}>
            <option value="">{t('all_sites')}</option>
            {sites.map((s) => (
              <option key={s} value={s}>
                {t('th_site')}: {s}
              </option>
            ))}
          </select>
        )}

        <select
          value={usableFilter}
          onChange={(e) => setUsableFilter(e.target.value as any)}
        >
          <option value="all">{t('all_status')}</option>
          <option value="usable">{t('usable_only')}</option>
          <option value="unusable">{t('unusable_only')}</option>
        </select>
      </div>

      {/* Responsive View: Desktop Table + Mobile Cards */}
      <div className="table-responsive hidden-mobile">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t('th_printer_id')}</th>
              <th>{t('th_display_name')}</th>
              <th>{t('th_site')}</th>
              <th>{t('th_model_driver')}</th>
              <th>{t('state')}</th>
              <th>{t('usable')}</th>
              <th>{t('th_queue')}</th>
              <th>{t('th_observed')}</th>
              <th>{t('actions')}</th>
            </tr>
          </thead>
          <tbody>
            {filteredPrinters.length > 0 ? (
              filteredPrinters.map((p) => {
                const st = p.status || { state: 'unknown' };
                return (
                  <tr
                    key={p.printer_id}
                    className="clickable-row"
                    onClick={() => onSelectPrinter(p.printer_id)}
                  >
                    <td>
                      <div className="flex items-center gap-1.5 font-mono font-medium">
                        <span>{p.printer_id}</span>
                        {p.custom_label && (
                          <span className="badge-pill badge-custom-label">
                            <Tag size={10} /> {p.custom_label}
                          </span>
                        )}
                      </div>
                    </td>
                    <td>
                      <div className="font-medium">{p.name || '—'}</div>
                      {p.remark && (
                        <div className="text-xs text-muted truncate max-w-xs">{p.remark}</div>
                      )}
                    </td>
                    <td>{p.site || '—'}</td>
                    <td className="font-mono text-sm">{p.model || p.driver}</td>
                    <td>
                      <span className={`state-badge state-${st.state}`}>
                        {tPrinterState(st.state)}
                      </span>
                    </td>
                    <td>
                      {p.usable ? (
                        <span className="badge-pill badge-ok">
                          <CheckCircle2 size={12} /> {t('yes')}
                        </span>
                      ) : (
                        <span className="badge-pill badge-danger">
                          <XCircle size={12} /> {t('no')}
                        </span>
                      )}
                    </td>
                    <td className="font-mono">{p.queue_depth || 0}</td>
                    <td className="text-muted text-sm">{formatAgo(st.observed_at)}</td>
                    <td onClick={(e) => e.stopPropagation()}>
                      <div className="table-btn-group">
                        <button
                          className="btn btn-xs btn-primary"
                          onClick={() => handleTestPrint(p.printer_id)}
                          title={t('btn_test')}
                        >
                          <Play size={11} /> {t('btn_test')}
                        </button>
                        <button
                          className="btn btn-xs btn-secondary"
                          onClick={() => onSelectPrinter(p.printer_id)}
                        >
                          {t('details')}
                        </button>
                      </div>
                    </td>
                  </tr>
                );
              })
            ) : (
              <tr>
                <td colSpan={9} className="text-center text-muted py-5">
                  {t('no_matching_printers')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {/* Mobile Card List (< 768px) */}
      <div className="visible-mobile-only grid-mobile-cards">
        {filteredPrinters.length > 0 ? (
          filteredPrinters.map((p) => {
            const st = p.status || { state: 'unknown' };
            return (
              <div
                key={p.printer_id}
                className={`mobile-card ${!p.usable ? 'tile-unusable' : ''}`}
                onClick={() => onSelectPrinter(p.printer_id)}
              >
                <div className="mobile-card-top">
                  <div>
                    <div className="flex items-center gap-1.5">
                      <span className="font-bold text-sm">{p.name || p.printer_id}</span>
                      {p.custom_label && (
                        <span className="badge-pill badge-custom-label text-xs">
                          {p.custom_label}
                        </span>
                      )}
                    </div>
                    <div className="text-xs text-muted font-mono mt-0.5">
                      {p.printer_id} · {p.site || '—'} · {p.model || p.driver}
                    </div>
                  </div>
                  <span className={`state-badge state-${st.state}`}>
                    {tPrinterState(st.state)}
                  </span>
                </div>

                {p.remark && <div className="mobile-card-remark">{p.remark}</div>}

                <div className="mobile-card-bottom">
                  <div className="flex items-center gap-1.5">
                    <span className={`badge-pill ${p.usable ? 'badge-ok' : 'badge-danger'}`}>
                      {p.usable ? t('usable') : t('not_usable')}
                    </span>
                    {p.queue_depth > 0 && (
                      <span className="badge-pill badge-info">Q: {p.queue_depth}</span>
                    )}
                  </div>
                  <button
                    className="btn btn-xs btn-primary"
                    onClick={(e) => {
                      e.stopPropagation();
                      handleTestPrint(p.printer_id);
                    }}
                  >
                    <Play size={11} /> {t('btn_test')}
                  </button>
                </div>
              </div>
            );
          })
        ) : (
          <div className="empty-box">{t('no_matching_printers')}</div>
        )}
      </div>

      {/* Printer Detail Modal */}
      {selectedPrinter && (
        <PrinterDetailModal
          printer={selectedPrinter}
          onClose={() => onSelectPrinter(null)}
          onTestPrint={() => handleTestPrint(selectedPrinter.printer_id)}
          onProbe={async () => {
            await api.probePrinters(selectedPrinter.printer_id);
            await refreshPrinters();
            toast(`Probed ${selectedPrinter.printer_id}`, 'success');
          }}
          onUpdated={async () => {
            await refreshPrinters();
          }}
        />
      )}
    </div>
  );
};

interface PrinterDetailModalProps {
  printer: Printer;
  onClose: () => void;
  onTestPrint: () => void;
  onProbe: () => void;
  onUpdated: () => void;
}

const PrinterDetailModal: React.FC<PrinterDetailModalProps> = ({
  printer: p,
  onClose,
  onTestPrint,
  onProbe,
  onUpdated,
}) => {
  const { t, tPrinterState } = useI18n();
  const { toast } = useToast();
  const st = p.status || { state: 'unknown' };

  const [isEditing, setIsEditing] = useState(false);
  const [name, setName] = useState(p.name || '');
  const [site, setSite] = useState(p.site || '');
  const [customLabel, setCustomLabel] = useState(p.custom_label || '');
  const [remark, setRemark] = useState(p.remark || '');
  const [enabled, setEnabled] = useState(p.enabled);
  const [saving, setSaving] = useState(false);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    try {
      await api.updatePrinter(p.printer_id, {
        name: name.trim(),
        site: site.trim(),
        custom_label: customLabel.trim(),
        remark: remark.trim(),
        enabled,
      });
      toast(t('metadata_updated'), 'success');
      setIsEditing(false);
      onUpdated();
    } catch (err: any) {
      toast(err.message || 'Failed to update printer', 'error');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-card ios-glass-sheet" onClick={(e) => e.stopPropagation()}>
        <div className="ios-grabber visible-mobile-only" />

        <div className="modal-header">
          <div className="modal-title-wrap">
            <PrinterIcon size={18} className="text-accent" />
            <h2>{p.name || p.printer_id}</h2>
          </div>
          <div className="flex items-center gap-2">
            <button
              className={`btn btn-xs ${isEditing ? 'btn-secondary' : 'btn-secondary'}`}
              onClick={() => setIsEditing(!isEditing)}
              title={t('edit_metadata')}
            >
              <FileEdit size={12} /> {isEditing ? t('cancel') : t('edit_metadata')}
            </button>
            <button className="modal-close-btn" onClick={onClose}>
              <X size={16} />
            </button>
          </div>
        </div>

        <div className="modal-body">
          {isEditing ? (
            <form onSubmit={handleSave} className="space-y-3">
              <div className="form-group">
                <label>{t('th_display_name')}</label>
                <input
                  type="text"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="e.g. Packing Station 2"
                />
              </div>

              <div className="form-group">
                <label>{t('custom_label')}</label>
                <input
                  type="text"
                  value={customLabel}
                  onChange={(e) => setCustomLabel(e.target.value)}
                  placeholder={t('custom_label_placeholder')}
                />
              </div>

              <div className="form-group">
                <label>{t('detail_site')}</label>
                <input
                  type="text"
                  value={site}
                  onChange={(e) => setSite(e.target.value)}
                  placeholder="e.g. wh1"
                />
              </div>

              <div className="form-group">
                <label>{t('remark')}</label>
                <textarea
                  rows={2}
                  value={remark}
                  onChange={(e) => setRemark(e.target.value)}
                  placeholder={t('remark_placeholder')}
                />
              </div>

              <div className="flex items-center gap-2 pt-1">
                <input
                  id="enabled-chk"
                  type="checkbox"
                  checked={enabled}
                  onChange={(e) => setEnabled(e.target.checked)}
                />
                <label htmlFor="enabled-chk" className="cursor-pointer text-sm font-medium">
                  {enabled ? t('usable') : t('not_usable')} ({t('state_disabled')})
                </label>
              </div>

              <div className="pt-2 flex justify-end gap-2">
                <button
                  type="button"
                  className="btn btn-secondary"
                  onClick={() => setIsEditing(false)}
                >
                  {t('cancel')}
                </button>
                <button type="submit" className="btn btn-primary" disabled={saving}>
                  <Save size={13} /> {saving ? t('save') : t('save_changes')}
                </button>
              </div>
            </form>
          ) : (
            <div className="detail-grid">
              <div className="detail-item">
                <span className="detail-label">{t('th_printer_id')}</span>
                <span className="detail-val font-mono">{p.printer_id}</span>
              </div>
              <div className="detail-item">
                <span className="detail-label">{t('status')}</span>
                <span className="detail-val">
                  <span className={`state-badge state-${st.state}`}>
                    {tPrinterState(st.state)}
                  </span>
                </span>
              </div>
              <div className="detail-item">
                <span className="detail-label">{t('usable')}</span>
                <span className="detail-val">
                  {p.usable ? (
                    <span className="badge-pill badge-ok">{t('usable')}</span>
                  ) : (
                    <span className="badge-pill badge-danger">{t('not_usable')}</span>
                  )}
                </span>
              </div>
              <div className="detail-item">
                <span className="detail-label">{t('th_queue')}</span>
                <span className="detail-val font-mono">{p.queue_depth || 0}</span>
              </div>
              <div className="detail-item">
                <span className="detail-label">{t('detail_endpoint')}</span>
                <span className="detail-val font-mono">
                  {p.host}:{p.port}
                </span>
              </div>
              <div className="detail-item">
                <span className="detail-label">{t('detail_model_driver')}</span>
                <span className="detail-val font-mono">
                  {p.model || 'Generic'} ({p.driver})
                </span>
              </div>
              <div className="detail-item">
                <span className="detail-label">{t('detail_site')}</span>
                <span className="detail-val">{p.site || '—'}</span>
              </div>
              <div className="detail-item">
                <span className="detail-label">{t('custom_label')}</span>
                <span className="detail-val">
                  {p.custom_label ? (
                    <span className="badge-pill badge-custom-label">{p.custom_label}</span>
                  ) : (
                    '—'
                  )}
                </span>
              </div>
              {p.remark && (
                <div className="detail-item col-span-2">
                  <span className="detail-label">{t('remark')}</span>
                  <span className="detail-val text-sm text-muted bg-panel-2 p-2 rounded-md border border-line">
                    {p.remark}
                  </span>
                </div>
              )}
              <div className="detail-item col-span-2">
                <span className="detail-label">{t('detail_observed_at')}</span>
                <span className="detail-val font-mono text-sm">
                  {st.observed_at ? new Date(st.observed_at).toLocaleString() : t('never')}
                </span>
              </div>
              {st.detail && (
                <div className="detail-item col-span-2">
                  <span className="detail-label">{t('detail_status_detail')}</span>
                  <span className="detail-val font-mono">{st.detail}</span>
                </div>
              )}
              {st.raw && (
                <div className="detail-item col-span-2">
                  <span className="detail-label">{t('detail_raw_hs')}</span>
                  <span className="detail-val font-mono text-xs code-box">{st.raw}</span>
                </div>
              )}
              {st.last_job && (
                <div className="detail-item col-span-2">
                  <span className="detail-label">{t('detail_last_job')}</span>
                  <span className="detail-val font-mono text-xs">
                    {st.last_job.job_id} ({st.last_job.state}) at{' '}
                    {new Date(st.last_job.at).toLocaleTimeString()}
                  </span>
                </div>
              )}
            </div>
          )}
        </div>

        <div className="modal-footer">
          <button className="btn btn-secondary" onClick={onProbe}>
            <RotateCw size={13} /> {t('btn_probe_now')}
          </button>
          <button className="btn btn-primary" onClick={onTestPrint}>
            <Play size={13} /> {t('btn_print_test_label')}
          </button>
        </div>
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
