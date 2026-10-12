import React, { useCallback, useState } from 'react';
import { AlertTriangle, CheckCircle2 } from 'lucide-react';
import { api, Diagnostics } from '../api';
import { useI18n } from '../i18n/context';
import { useToast } from './Toast';

// Diagnostics state lives in a hook so the dialog's tab bar can own the Run button.
// onDone runs after each run: diagnostics may record verified facts on the printer.
export function useDiagnostics(printerId: string, onDone?: () => void) {
  const { toast } = useToast();
  const [result, setResult] = useState<Diagnostics | null>(null);
  const [running, setRunning] = useState(false);
  const run = useCallback(async () => {
    setRunning(true);
    try {
      setResult(await api.diagnostics(printerId));
      onDone?.();
    } catch (e: any) {
      toast(e.message || 'Diagnostics failed', 'error');
    } finally {
      setRunning(false);
    }
  }, [printerId, toast, onDone]);
  return { result, running, run };
}

// Asks the printer itself (~HI, ~HS, ^HH, ^HW) how it is set up and shows where that
// disagrees with the declared spec. Read-only: changes are made in the Edit tab.
export const DiagnosticsPanel: React.FC<{
  diag: ReturnType<typeof useDiagnostics>;
}> = ({ diag: state }) => {
  const { t } = useI18n();
  const diag = state.result;
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
            {(diag.recorded || []).map((r) => (
              <span key={r} className="badge-pill badge-ok text-xs">
                <CheckCircle2 size={12} /> {t('diag_recorded', { what: r })}
              </span>
            ))}
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
            {row(
              t('diag_cjk_fonts'),
              diag.cjk_fonts?.length
                ? diag.cjk_fonts.map((f) => f.name).join(', ')
                : t('diag_none'),
            )}
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
