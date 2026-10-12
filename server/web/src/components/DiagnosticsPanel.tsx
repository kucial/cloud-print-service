import React, { useCallback, useEffect, useState } from 'react';
import { AlertTriangle, CheckCircle2, Crosshair, Info, Play, Save } from 'lucide-react';
import { api, Diagnostics } from '../api';
import { useI18n } from '../i18n/context';
import { useToast } from './Toast';

// Diagnostics state lives in a hook so the dialog's tab bar can own the Run button.
export function useDiagnostics(printerId: string) {
  const { toast } = useToast();
  const [result, setResult] = useState<Diagnostics | null>(null);
  const [running, setRunning] = useState(false);
  const run = useCallback(async () => {
    setRunning(true);
    try {
      setResult(await api.diagnostics(printerId));
    } catch (e: any) {
      toast(e.message || 'Diagnostics failed', 'error');
    } finally {
      setRunning(false);
    }
  }, [printerId, toast]);
  return { result, running, run };
}

// Asks the printer itself (~HI, ~HS, ^HH) how it is set up and shows where that
// disagrees with the declared spec. Offsets are a small form saved to the printer;
// calibrate feeds labels, so it needs a second click.
export const DiagnosticsPanel: React.FC<{
  printerId: string;
  diag: ReturnType<typeof useDiagnostics>;
  onTestPrint: () => void;
}> = ({ printerId, diag: state, onTestPrint }) => {
  const { t } = useI18n();
  const { toast } = useToast();
  const diag = state.result;
  const [calibrateArmed, setCalibrateArmed] = useState(false);
  const [top, setTop] = useState(0);
  const [left, setLeft] = useState(0);
  const [saving, setSaving] = useState(false);
  const savedTop = diag?.config?.label_top_dots ?? 0;
  const savedLeft = diag?.config?.left_position_dots ?? 0;
  const dirty = top !== savedTop || left !== savedLeft;

  // Offsets start from what the printer has saved.
  useEffect(() => {
    setTop(diag?.config?.label_top_dots ?? 0);
    setLeft(diag?.config?.left_position_dots ?? 0);
  }, [diag]);

  const calibrate = async () => {
    if (!calibrateArmed) {
      setCalibrateArmed(true);
      return;
    }
    setCalibrateArmed(false);
    try {
      const res = await api.maintenance(printerId, 'calibrate');
      toast(t('diag_sent', { cmd: res.sent, note: res.note }), 'success');
      void state.run();
    } catch (e: any) {
      toast(e.message || 'Calibration failed', 'error');
    }
  };

  // Sends only the offsets that changed, then reads them back from the printer.
  const saveOffsets = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    try {
      if (top !== savedTop) await api.maintenance(printerId, 'label_top', top);
      if (left !== savedLeft) await api.maintenance(printerId, 'left_shift', left);
      toast(t('diag_offsets_saved'), 'success');
      await state.run();
    } catch (err: any) {
      toast(err.message || 'Saving offsets failed', 'error');
    } finally {
      setSaving(false);
    }
  };

  const c = diag?.config;
  const row = (label: string, value?: React.ReactNode) =>
    value === undefined || value === '' ? null : (
      <div className="detail-item">
        <span className="detail-label">{label}</span>
        <span className="detail-val font-mono text-sm">{value}</span>
      </div>
    );

  return (
    <div>
      <p className="text-muted text-xs">{t('diag_hint')}</p>
      {!diag && (
        <p className="text-muted text-sm mt-2">{state.running ? t('diag_running') : '—'}</p>
      )}

      {diag && (
        <>
          <div className="mt-2 flex flex-col gap-1">
            {diag.warnings.length === 0 ? (
              <span className="badge-pill badge-ok text-xs">
                <CheckCircle2 size={12} /> {t('diag_no_warnings')}
              </span>
            ) : (
              diag.warnings.map((w) => (
                <span key={w.code} className="badge-pill badge-warn text-xs">
                  <AlertTriangle size={12} /> {w.message}
                </span>
              ))
            )}
            {Object.entries(diag.errors || {}).map(([q, err]) => (
              <span key={q} className="badge-pill badge-danger text-xs">
                {t('diag_query_failed', { q, err })}
              </span>
            ))}
          </div>

          <div className="detail-grid mt-2">
            {row(
              t('diag_firmware'),
              diag.identity && `${diag.identity.model} · ${diag.identity.firmware ?? ''}`,
            )}
            {c && row(t('dpi_label'), `${c.dpi} DPI (${c.dpmm} dpmm)`)}
            {c && row(t('diag_print_width'), `${c.print_width_mm} mm (${c.print_width_dots} dots)`)}
            {c &&
              row(t('diag_label_pitch'), `${c.label_length_mm} mm (${c.label_length_dots} dots)`)}
            {c && row(t('diag_label_top'), `${c.label_top_dots} ${t('diag_dots')}`)}
            {c && row(t('diag_left_shift'), `${c.left_position_dots} ${t('diag_dots')}`)}
            {c &&
              row(
                t('diag_media'),
                `${c.media_type ?? '—'} · ${c.print_method ?? '—'} · ${c.print_mode ?? '—'}`,
              )}
            {c && row(t('diag_darkness_speed'), `${c.darkness ?? '—'} · ${c.print_speed ?? '—'}`)}
            {c && row(t('diag_language'), c.language)}
            {c && row(t('diag_head_usage'), c.head_usage)}
          </div>

          <form className="adjust-card" onSubmit={saveOffsets}>
            <div className="section-title">{t('diag_offsets_title')}</div>
            <div className="grid grid-cols-2 gap-2 mt-2">
              <div className="form-group mb-0">
                <label htmlFor="offset-top">{t('diag_label_top_field')}</label>
                <input
                  id="offset-top"
                  type="number"
                  min={-120}
                  max={120}
                  value={top}
                  onChange={(e) => setTop(Number(e.target.value))}
                />
                <span className="text-xs text-muted">{t('diag_label_top_hint')}</span>
              </div>
              <div className="form-group mb-0">
                <label htmlFor="offset-left">{t('diag_left_shift_field')}</label>
                <input
                  id="offset-left"
                  type="number"
                  min={-120}
                  max={120}
                  value={left}
                  onChange={(e) => setLeft(Number(e.target.value))}
                />
                <span className="text-xs text-muted">{t('diag_left_shift_hint')}</span>
              </div>
            </div>
            <p className="text-xs text-muted mt-2 flex gap-1">
              <Info size={13} style={{ flexShrink: 0 }} /> {t('diag_offsets_note')}
            </p>
            <div className="mt-2 flex justify-end gap-2 flex-wrap">
              <button type="button" className="btn btn-secondary btn-sm" onClick={onTestPrint}>
                <Play size={13} /> {t('btn_print_alignment_label')}
              </button>
              <button type="submit" className="btn btn-primary btn-sm" disabled={!dirty || saving}>
                <Save size={13} /> {saving ? t('save') : t('diag_offsets_save')}
              </button>
            </div>
          </form>

          <div className="adjust-card flex items-center justify-between gap-2 flex-wrap">
            <div>
              <div className="section-title">{t('diag_calibrate')}</div>
              <p className="text-xs text-muted">{t('diag_calibrate_note')}</p>
            </div>
            <button
              className={`btn btn-sm ${calibrateArmed ? 'btn-danger' : 'btn-secondary'}`}
              onClick={calibrate}
            >
              <Crosshair size={13} />{' '}
              {calibrateArmed ? t('diag_calibrate_confirm') : t('diag_calibrate')}
            </button>
          </div>

          {diag.raw_config && (
            <details className="mt-2">
              <summary className="text-xs text-muted cursor-pointer">{t('diag_raw')}</summary>
              <pre className="code-box text-xs" style={{ whiteSpace: 'pre', overflowX: 'auto' }}>
                {diag.raw_config.replace(/[\u0002\u0003]/g, '')}
              </pre>
            </details>
          )}
        </>
      )}
    </div>
  );
};
