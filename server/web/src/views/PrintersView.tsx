import React, { useEffect, useState } from 'react';
import { useApp } from '../context/AppContext';
import { useI18n } from '../i18n/context';
import { Printer } from '../types';
import { api } from '../api';
import { useToast } from '../components/Toast';
import { DiagnosticsPanel, useDiagnostics } from '../components/DiagnosticsPanel';
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
                      <div className="flex flex-col gap-1 items-start">
                        {p.paper_size ? (
                          <span className="badge-pill badge-warn font-mono">{p.paper_size}</span>
                        ) : (
                          <span className="text-muted text-xs">—</span>
                        )}
                        <div className="flex items-center gap-1 flex-wrap">
                          <span
                            className={`badge-pill text-xs font-mono ${p.dpi_resolved_from_hi ? 'badge-ok' : ''}`}
                            style={{ fontSize: '10px', padding: '1px 5px' }}
                            title={
                              p.dpi_resolved_from_hi
                                ? t('verified_from_hi')
                                : t('declared_in_config')
                            }
                          >
                            {p.dpi || 203} DPI {p.dpi_resolved_from_hi ? '✓~HI' : ''}
                          </span>
                          {(p.cjk_supported || p.cjk_font) && (
                            <span
                              className={`badge-pill text-xs font-mono ${p.font_resolved_from_hi ? 'badge-ok' : 'badge-primary'}`}
                              style={{ fontSize: '10px', padding: '1px 5px' }}
                              title={`${p.cjk_font || 'CJK'} (${p.font_resolved_from_hi ? t('verified_from_hi') : t('declared_in_config')})`}
                            >
                              中 {p.cjk_font || 'CJK'} {p.font_resolved_from_hi ? '✓~HI' : ''}
                            </span>
                          )}
                        </div>
                      </div>
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
                      <span className="badge-pill text-xs font-mono">{p.dpi || 203} DPI</span>
                      {(p.cjk_supported || p.cjk_font) && (
                        <span className="badge-pill badge-primary text-xs font-mono">
                          中 {p.cjk_font || 'CJK'}
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
  const { toast } = useToast();
  const st = p.status || { state: 'unknown' };

  const [tab, setTab] = useState<'info' | 'edit' | 'diag'>('info');
  const diag = useDiagnostics(p.printer_id, onUpdated);
  // Diagnostics are read-only and quick: run them the first time the tab opens.
  useEffect(() => {
    if (tab === 'diag' && !diag.result && !diag.running) void diag.run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab]);
  const [name, setName] = useState(p.name || '');
  const [site, setSite] = useState(p.site || '');
  const [customLabel, setCustomLabel] = useState(p.custom_label || '');
  const [remark, setRemark] = useState(p.remark || '');
  const [paperSize, setPaperSize] = useState(p.paper_size || '');
  const [mediaWidthMm, setMediaWidthMm] = useState<number | undefined>(p.media_width_mm);
  const [mediaHeightMm, setMediaHeightMm] = useState<number | undefined>(p.media_height_mm);
  const [dpi, setDpi] = useState<number | undefined>(p.dpi);
  const [dpmm, setDpmm] = useState<number | undefined>(p.dpmm);
  const [orientation, setOrientation] = useState(p.orientation || '');
  const [printDirection, setPrintDirection] = useState(p.print_direction || '');
  const [cjkFont, setCjkFont] = useState(p.cjk_font || '');
  const [cjkSupported, setCjkSupported] = useState(p.cjk_supported || false);
  const [enabled, setEnabled] = useState(p.enabled);
  const [saving, setSaving] = useState(false);

  const handlePaperSizeChange = (val: string) => {
    setPaperSize(val);
    const cleaned = val.toLowerCase().trim();
    if (cleaned === '4x6' || cleaned === '4x6in') {
      setMediaWidthMm(101.6);
      setMediaHeightMm(152.4);
    } else if (cleaned === '4x2' || cleaned === '4x2in') {
      setMediaWidthMm(101.6);
      setMediaHeightMm(50.8);
    } else if (cleaned === '2x1' || cleaned === '2x1in') {
      setMediaWidthMm(50.8);
      setMediaHeightMm(25.4);
    } else {
      const match = cleaned.match(/^(\d+(?:\.\d+)?)\s*[x*]\s*(\d+(?:\.\d+)?)\s*(mm)?$/);
      if (match) {
        setMediaWidthMm(parseFloat(match[1]));
        setMediaHeightMm(parseFloat(match[2]));
      }
    }
  };

  const handleDPIChange = (val: number | undefined) => {
    setDpi(val);
    if (val === 203) setDpmm(8);
    else if (val === 300) setDpmm(12);
    else if (val === 600) setDpmm(24);
    else if (val) setDpmm(Math.round((val / 25.4) * 10) / 10);
  };

  const handleCjkFontChange = (val: string) => {
    setCjkFont(val);
    if (val.trim()) setCjkSupported(true);
  };

  const resetForm = () => {
    setName(p.name || '');
    setSite(p.site || '');
    setCustomLabel(p.custom_label || '');
    setRemark(p.remark || '');
    setPaperSize(p.paper_size || '');
    setMediaWidthMm(p.media_width_mm);
    setMediaHeightMm(p.media_height_mm);
    setDpi(p.dpi);
    setDpmm(p.dpmm);
    setOrientation(p.orientation || '');
    setPrintDirection(p.print_direction || '');
    setCjkFont(p.cjk_font || '');
    setCjkSupported(p.cjk_supported || false);
    setEnabled(p.enabled);
  };

  // Load the latest record each time Edit opens: diagnostics may have filled in fields
  // since the dialog opened, and saving a stale form would undo them.
  useEffect(() => {
    if (tab === 'edit') resetForm();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    try {
      await api.updatePrinter(p.printer_id, {
        name: name.trim(),
        site: site.trim(),
        custom_label: customLabel.trim(),
        remark: remark.trim(),
        paper_size: paperSize.trim(),
        media_width_mm: mediaWidthMm,
        media_height_mm: mediaHeightMm,
        dpi,
        dpmm,
        orientation,
        media_orientation: orientation,
        print_direction: printDirection,
        cjk_supported: cjkSupported,
        cjk_font: cjkFont.trim(),
        enabled,
      });
      toast(t('metadata_updated'), 'success');
      setTab('info');
      onUpdated();
    } catch (err: any) {
      toast(err.message || 'Failed to update printer', 'error');
    } finally {
      setSaving(false);
    }
  };

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
            {tab === 'edit' && (
              <>
                <button type="button" className="btn btn-secondary btn-sm" onClick={resetForm}>
                  {t('tab_reset')}
                </button>
                <button
                  type="submit"
                  form="printer-edit-form"
                  className="btn btn-primary btn-sm"
                  disabled={saving}
                >
                  <Save size={13} /> {saving ? t('save') : t('save_changes')}
                </button>
              </>
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
            <form id="printer-edit-form" onSubmit={handleSave} className="space-y-3">
              <div className="grid grid-cols-2 gap-2">
                <div className="form-group mb-0">
                  <label>{t('th_display_name')}</label>
                  <input
                    type="text"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder="e.g. Packing Station 2"
                  />
                </div>

                <div className="form-group mb-0">
                  <label>{t('custom_label')}</label>
                  <input
                    type="text"
                    value={customLabel}
                    onChange={(e) => setCustomLabel(e.target.value)}
                    placeholder={t('custom_label_placeholder')}
                  />
                </div>
              </div>

              <div className="grid grid-cols-3 gap-2">
                <div className="form-group mb-0">
                  <label>{t('paper_size')}</label>
                  <input
                    type="text"
                    value={paperSize}
                    onChange={(e) => handlePaperSizeChange(e.target.value)}
                    placeholder="e.g. 100x150mm, 4x6"
                  />
                </div>
                <div className="form-group mb-0">
                  <label>W (mm)</label>
                  <input
                    type="number"
                    step="0.1"
                    value={mediaWidthMm ?? ''}
                    onChange={(e) => setMediaWidthMm(parseFloat(e.target.value) || undefined)}
                    placeholder="100.0"
                  />
                </div>
                <div className="form-group mb-0">
                  <label>H (mm)</label>
                  <input
                    type="number"
                    step="0.1"
                    value={mediaHeightMm ?? ''}
                    onChange={(e) => setMediaHeightMm(parseFloat(e.target.value) || undefined)}
                    placeholder="150.0"
                  />
                </div>
              </div>

              <div className="grid grid-cols-2 gap-2">
                <div className="form-group mb-0">
                  <label>{t('dpi_label')}</label>
                  <select
                    value={dpi ?? 203}
                    onChange={(e) => handleDPIChange(parseInt(e.target.value) || undefined)}
                  >
                    <option value={203}>203 DPI (8 dpmm)</option>
                    <option value={300}>300 DPI (12 dpmm)</option>
                    <option value={600}>600 DPI (24 dpmm)</option>
                  </select>
                </div>
                <div className="form-group mb-0">
                  <label>{t('dpmm_label')}</label>
                  <input
                    type="number"
                    step="0.1"
                    value={dpmm ?? 8}
                    onChange={(e) => setDpmm(parseFloat(e.target.value) || undefined)}
                    placeholder="8"
                  />
                </div>
              </div>

              <div className="grid grid-cols-2 gap-2">
                <div className="form-group mb-0">
                  <label>{t('orientation_label')}</label>
                  <select value={orientation} onChange={(e) => setOrientation(e.target.value)}>
                    <option value="">{t('not_set')}</option>
                    <option value="normal">{t('orientation_normal')}</option>
                    <option value="inverted">{t('orientation_inverted')}</option>
                    <option value="portrait">{t('orientation_portrait')}</option>
                    <option value="landscape">{t('orientation_landscape')}</option>
                  </select>
                </div>
                <div className="form-group mb-0">
                  <label>{t('print_direction_label')}</label>
                  <select
                    value={printDirection}
                    onChange={(e) => setPrintDirection(e.target.value)}
                  >
                    <option value="">{t('not_set')}</option>
                    <option value="top_first">{t('direction_top_first')}</option>
                    <option value="bottom_first">{t('direction_bottom_first')}</option>
                  </select>
                </div>
              </div>

              <div className="form-group">
                <label>{t('cjk_font_label')}</label>
                <input
                  type="text"
                  value={cjkFont}
                  onChange={(e) => handleCjkFontChange(e.target.value)}
                  placeholder="e.g. SIMSUN.FNT, ANSD.TTF, NotoSansSC"
                />
              </div>

              <div className="flex items-center gap-2">
                <input
                  id="cjk-chk"
                  type="checkbox"
                  checked={cjkSupported}
                  onChange={(e) => setCjkSupported(e.target.checked)}
                />
                <label htmlFor="cjk-chk" className="cursor-pointer text-sm font-medium">
                  {t('cjk_supported_label')}
                </label>
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
            </form>
          )}
          {tab === 'info' && (
            <div className="detail-grid">
              <div className="detail-item">
                <span className="detail-label">{t('th_printer_id')}</span>
                <span className="detail-val font-mono">{p.printer_id}</span>
              </div>
              <div className="detail-item">
                <span className="detail-label">{t('status')}</span>
                <span className="detail-val">
                  <span className={`state-badge state-${st.state}`}>{tPrinterState(st.state)}</span>
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
              <div className="detail-item">
                <span className="detail-label">{t('cjk_font_label')}</span>
                <span className="detail-val text-sm font-mono flex items-center gap-1.5 flex-wrap">
                  {p.cjk_font ? (
                    <span className="badge-pill badge-primary">中 {p.cjk_font}</span>
                  ) : p.cjk_supported ? (
                    <span className="badge-pill badge-ok">CJK Ready</span>
                  ) : (
                    '—'
                  )}
                  {(p.cjk_font || p.cjk_supported) && (
                    <span
                      className={`badge-pill text-xs ${p.font_resolved_from_hi ? 'badge-ok' : 'badge-neutral'}`}
                    >
                      {p.font_resolved_from_hi ? t('verified_from_hi') : t('declared_in_config')}
                    </span>
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
          {tab === 'diag' && <DiagnosticsPanel printerId={p.printer_id} diag={diag} onTestPrint={onTestPrint} />}
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
