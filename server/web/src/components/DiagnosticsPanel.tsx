import React, { useState } from 'react';
import { Stethoscope, AlertTriangle, CheckCircle2, Crosshair, MoveVertical, MoveHorizontal } from 'lucide-react';
import { api, Diagnostics } from '../api';
import { useI18n } from '../i18n/context';
import { useToast } from './Toast';

// Asks the printer itself (~HI, ~HS, ^HH) how it is set up and shows where that
// disagrees with the declared spec. Calibrate and the offsets change the printer, so each
// needs a second click to confirm.
export const DiagnosticsPanel: React.FC<{ printerId: string }> = ({ printerId }) => {
  const { t } = useI18n();
  const { toast } = useToast();
  const [diag, setDiag] = useState<Diagnostics | null>(null);
  const [running, setRunning] = useState(false);
  type Action = 'calibrate' | 'label_top' | 'left_shift';
  const [armed, setArmed] = useState<Action | null>(null);
  const [top, setTop] = useState(0);
  const [left, setLeft] = useState(0);

  const run = async () => {
    setRunning(true);
    try {
      const d = await api.diagnostics(printerId);
      setDiag(d);
      setTop(d.config?.label_top_dots ?? 0);
      setLeft(d.config?.left_position_dots ?? 0);
    } catch (e: any) {
      toast(e.message || 'Diagnostics failed', 'error');
    } finally {
      setRunning(false);
    }
  };

  const act = async (action: Action) => {
    if (armed !== action) {
      setArmed(action);
      return;
    }
    setArmed(null);
    try {
      const dots = action === 'label_top' ? top : action === 'left_shift' ? left : undefined;
      const res = await api.maintenance(printerId, action, dots);
      toast(t('diag_sent', { cmd: res.sent, note: res.note }), 'success');
    } catch (e: any) {
      toast(e.message || 'Maintenance failed', 'error');
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
    <div className="pt-3 mt-3" style={{ borderTop: '1px solid var(--line)' }}>
      <div className="flex items-center justify-between gap-2">
        <div>
          <div className="section-title">{t('diag_title')}</div>
          <p className="text-muted text-xs">{t('diag_hint')}</p>
        </div>
        <button className="btn btn-secondary btn-sm" onClick={run} disabled={running}>
          <Stethoscope size={13} /> {running ? t('diag_running') : t('diag_run')}
        </button>
      </div>

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
            {row(t('diag_firmware'), diag.identity && `${diag.identity.model} · ${diag.identity.firmware ?? ''}`)}
            {c && row(t('dpi_label'), `${c.dpi} DPI (${c.dpmm} dpmm)`)}
            {c && row(t('diag_print_width'), `${c.print_width_mm} mm (${c.print_width_dots} dots)`)}
            {c && row(t('diag_label_pitch'), `${c.label_length_mm} mm (${c.label_length_dots} dots)`)}
            {c && row(t('diag_label_top'), `${c.label_top_dots} ${t('diag_dots')}`)}
            {c && row(t('diag_left_shift'), `${c.left_position_dots} ${t('diag_dots')}`)}
            {c && row(t('diag_media'), `${c.media_type ?? '—'} · ${c.print_method ?? '—'} · ${c.print_mode ?? '—'}`)}
            {c && row(t('diag_darkness_speed'), `${c.darkness ?? '—'} · ${c.print_speed ?? '—'}`)}
            {c && row(t('diag_language'), c.language)}
            {c && row(t('diag_head_usage'), c.head_usage)}
          </div>

          <div className="mt-2 flex items-center gap-2 flex-wrap">
            <button
              className={`btn btn-sm ${armed === 'calibrate' ? 'btn-danger' : 'btn-secondary'}`}
              onClick={() => act('calibrate')}
            >
              <Crosshair size={13} /> {armed === 'calibrate' ? t('diag_calibrate_confirm') : t('diag_calibrate')}
            </button>
          </div>
          <OffsetRow
            icon={<MoveVertical size={13} />}
            label={t('diag_label_top')}
            hint={t('diag_label_top_hint')}
            value={top}
            onChange={(v) => {
              setTop(v);
              setArmed(null);
            }}
            armed={armed === 'label_top'}
            onApply={() => act('label_top')}
          />
          <OffsetRow
            icon={<MoveHorizontal size={13} />}
            label={t('diag_left_shift')}
            hint={t('diag_left_shift_hint')}
            value={left}
            onChange={(v) => {
              setLeft(v);
              setArmed(null);
            }}
            armed={armed === 'left_shift'}
            onApply={() => act('left_shift')}
          />

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

// One saved printer offset: dots input, apply button that needs a second click.
const OffsetRow: React.FC<{
  icon: React.ReactNode;
  label: string;
  hint: string;
  value: number;
  onChange: (v: number) => void;
  armed: boolean;
  onApply: () => void;
}> = ({ icon, label, hint, value, onChange, armed, onApply }) => {
  const { t } = useI18n();
  return (
    <div className="mt-2 flex items-center gap-2 flex-wrap">
      <input
        type="number"
        min={-120}
        max={120}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        style={{ width: 80 }}
        aria-label={label}
      />
      <button className={`btn btn-sm ${armed ? 'btn-danger' : 'btn-secondary'}`} onClick={onApply}>
        {icon} {armed ? t('diag_offset_confirm') : t('diag_offset_apply', { what: label })}
      </button>
      <span className="text-xs text-muted">{hint}</span>
    </div>
  );
};
