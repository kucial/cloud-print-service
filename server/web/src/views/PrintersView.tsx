import React, { useEffect, useState } from 'react';
import { useApp } from '../context/AppContext';
import { useI18n } from '../i18n/context';
import { Printer } from '../types';
import { api } from '../api';
import { useToast } from '../components/Toast';
import { DiagnosticsPanel, useDiagnostics } from '../components/DiagnosticsPanel';
import { PrinterSettings } from '../components/PrinterSettings';
import { FontsSection } from '../components/FontsSection';
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
  Info,
  Stethoscope,
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
  const [paperSizeFilter, setPaperSizeFilter] = useState('');
  const [usableFilter, setUsableFilter] = useState<'all' | 'usable' | 'unusable'>('all');
  const [probing, setProbing] = useState(false);

  const selectedPrinter = printers.find((p) => p.printer_id === selectedPrinterId);
  const sites = Array.from(new Set(printers.map((p) => p.site).filter(Boolean)));
  const paperSizes = Array.from(
    new Set(printers.map((p) => p.paper_size).filter(Boolean)),
  ) as string[];

  const filteredPrinters = printers.filter((p) => {
    const q = search.toLowerCase();
    const matchesSearch =
      p.printer_id.toLowerCase().includes(q) ||
      (p.name && p.name.toLowerCase().includes(q)) ||
      (p.model && p.model.toLowerCase().includes(q)) ||
      (p.custom_label && p.custom_label.toLowerCase().includes(q)) ||
      (p.paper_size && p.paper_size.toLowerCase().includes(q)) ||
      (p.remark && p.remark.toLowerCase().includes(q));

    const matchesSite = !siteFilter || p.site === siteFilter;
    const matchesPaperSize = !paperSizeFilter || p.paper_size === paperSizeFilter;
    const matchesUsable =
      usableFilter === 'all' ||
      (usableFilter === 'usable' && p.usable) ||
      (usableFilter === 'unusable' && !p.usable);

    return matchesSearch && matchesSite && matchesPaperSize && matchesUsable;
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
          <button className="btn btn-secondary" onClick={handleProbeAll} disabled={probing}>
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

        {paperSizes.length > 0 && (
          <select value={paperSizeFilter} onChange={(e) => setPaperSizeFilter(e.target.value)}>
            <option value="">{t('all_paper_sizes')}</option>
            {paperSizes.map((ps) => (
              <option key={ps} value={ps}>
                {t('paper_size_filter')}: {ps}
              </option>
            ))}
          </select>
        )}

        <select value={usableFilter} onChange={(e) => setUsableFilter(e.target.value as any)}>
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
              <th>{t('paper_size')}</th>
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
                    <td>
                      {p.paper_size ? (
                        <span className="badge-pill badge-warn font-mono">{p.paper_size}</span>
                      ) : (
                        <span className="text-muted text-xs">—</span>
                      )}
                    </td>
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
                      {p.paper_size && (
                        <span className="badge-pill badge-warn text-xs font-mono">
                          {p.paper_size}
                        </span>
                      )}
                    </div>
                    <div className="text-xs text-muted font-mono mt-0.5">
                      {p.printer_id} · {p.site || '—'} · {p.model || p.driver}
                    </div>
                  </div>
                  <span className={`state-badge state-${st.state}`}>{tPrinterState(st.state)}</span>
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
  const st = p.status || { state: 'unknown' };

  const [tab, setTab] = useState<'info' | 'edit' | 'diag'>('info');
  const diag = useDiagnostics(p.printer_id, onUpdated);
  // Diagnostics are read-only and quick: run them the first time the tab opens.
  useEffect(() => {
    if (tab === 'diag' && !diag.result && !diag.running) void diag.run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab]);
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal-card modal-lg printer-modal ios-glass-sheet"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="ios-grabber visible-mobile-only" />

        <div className="modal-header">
          <div className="modal-title-wrap">
            <PrinterIcon size={18} className="text-accent" />
            <h2>{p.name || p.printer_id}</h2>
          </div>
          <div className="flex items-center gap-2">
            <span className={`badge-pill ${p.usable ? 'badge-ok' : 'badge-danger'}`}>
              {tPrinterState(st.state)}
            </span>
            <button className="modal-close-btn" onClick={onClose}>
              <X size={16} />
            </button>
          </div>
        </div>

        <div className="modal-tabbar">
          <div className="modal-tabs">
            {(
              [
                ['info', <Info size={13} key="i" />, t('tab_info')],
                ['edit', <FileEdit size={13} key="e" />, t('tab_edit')],
                ['diag', <Stethoscope size={13} key="d" />, t('diag_title')],
              ] as const
            ).map(([id, icon, label]) => (
              <button
                key={id}
                className={`nav-tab ${tab === id ? 'active' : ''}`}
                onClick={() => setTab(id)}
              >
                {icon} {label}
              </button>
            ))}
          </div>
          <div className="flex items-center gap-2">
            {tab === 'info' && (
              <button className="btn btn-primary btn-sm" onClick={onProbe}>
                <RotateCw size={13} /> {t('btn_probe_now')}
              </button>
            )}
            {tab === 'diag' && (
              <>
                <button
                  className="btn btn-primary btn-sm"
                  onClick={() => void diag.run()}
                  disabled={diag.running}
                >
                  <Stethoscope size={13} /> {diag.running ? t('diag_running') : t('diag_run')}
                </button>
              </>
            )}
          </div>
        </div>

        <div className="modal-body">
          {tab === 'edit' && (
            <PrinterSettings
              printer={p}
              diag={diag}
              onUpdated={onUpdated}
              onTestPrint={onTestPrint}
            />
          )}
          {tab === 'info' && (
            <>
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
                <div className="detail-item">
                  <span className="detail-label">{t('paper_size')}</span>
                  <span className="detail-val">
                    {p.paper_size ? (
                      <span className="badge-pill badge-warn font-mono">
                        {p.paper_size}{' '}
                        {p.media_width_mm && p.media_height_mm
                          ? `(${p.media_width_mm}×${p.media_height_mm}mm)`
                          : ''}
                      </span>
                    ) : (
                      '—'
                    )}
                  </span>
                </div>
                <div className="detail-item">
                  <span className="detail-label">{t('dpi_label')}</span>
                  <span className="detail-val font-mono flex items-center gap-1.5 flex-wrap">
                    <span>
                      {p.dpi || 203} DPI ({p.dpmm || 8} dpmm)
                    </span>
                    <span
                      className={`badge-pill text-xs ${p.dpi_resolved_from_hi ? 'badge-ok' : 'badge-neutral'}`}
                    >
                      {p.dpi_resolved_from_hi ? t('verified_from_hi') : t('declared_in_config')}
                    </span>
                  </span>
                </div>
                <div className="detail-item">
                  <span className="detail-label">{t('orientation_label')}</span>
                  <span className="detail-val text-sm font-mono">
                    {p.orientation || t('not_set')} ·{' '}
                    {p.print_direction === 'bottom_first'
                      ? t('direction_bottom_first')
                      : p.print_direction === 'top_first'
                        ? t('direction_top_first')
                        : t('not_set')}
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
              <FontsSection fonts={p.available_fonts} />
            </>
          )}
          {tab === 'diag' && <DiagnosticsPanel diag={diag} />}
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
