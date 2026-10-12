import React, { useEffect, useState } from 'react';
import { Crosshair, Info, Play, RotateCcw, Save } from 'lucide-react';
import { api } from '../api';
import { Printer } from '../types';
import { useI18n } from '../i18n/context';
import { useToast } from './Toast';
import { useDiagnostics } from './DiagnosticsPanel';

// Everything editable about a printer, in sections that save independently: two are
// CPS's own record, one writes to the printer itself. Keeping the saves apart means a
// rename can never also rewrite the printer's offsets, and each section says where its
// values live.
export const PrinterSettings: React.FC<{
  printer: Printer;
  diag: ReturnType<typeof useDiagnostics>;
  onUpdated: () => void;
  onTestPrint: () => void;
}> = ({ printer, diag, onUpdated, onTestPrint }) => {
  // The printer's saved offsets come from its configuration dump; read it once.
  useEffect(() => {
    if (!diag.result && !diag.running) void diag.run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <div className="space-y-3">
      <BasicsSection p={printer} onUpdated={onUpdated} />
      <MediaSection p={printer} onUpdated={onUpdated} />
      <PrinterSideSection p={printer} diag={diag} onTestPrint={onTestPrint} />
    </div>
  );
};

// A form section with its own Save, enabled only once something changed.
const Section: React.FC<{
  title: string;
  where: string;
  dirty: boolean;
  saving: boolean;
  saveLabel?: string;
  onSave: () => void;
  onReset: () => void;
  extraActions?: React.ReactNode;
  children: React.ReactNode;
}> = ({ title, where, dirty, saving, saveLabel, onSave, onReset, extraActions, children }) => {
  const { t } = useI18n();
  return (
    <form
      className="adjust-card"
      onSubmit={(e) => {
        e.preventDefault();
        onSave();
      }}
    >
      <div className="flex items-center justify-between gap-2 flex-wrap">
        <div className="section-title">{title}</div>
        <span className="text-xs text-muted">{where}</span>
      </div>
      <div className="space-y-3 mt-2">{children}</div>
      <div className="mt-3 flex justify-end gap-2 flex-wrap">
        {extraActions}
        {dirty && (
          <button type="button" className="btn btn-secondary btn-sm" onClick={onReset}>
            <RotateCcw size={13} /> {t('tab_reset')}
          </button>
        )}
        <button type="submit" className="btn btn-primary btn-sm" disabled={!dirty || saving}>
          <Save size={13} /> {saving ? t('save') : saveLabel || t('save')}
        </button>
      </div>
    </form>
  );
};

// useSectionForm keeps a section's draft in sync with the record until the user edits
// it, so values recorded elsewhere (diagnostics, another admin) show up instead of a
// stale copy being saved back over them.
function useSectionForm<T extends Record<string, unknown>>(fromRecord: T) {
  const key = JSON.stringify(fromRecord);
  const [draft, setDraft] = useState<T>(fromRecord);
  const [base, setBase] = useState(key);
  const dirty = JSON.stringify(draft) !== base;
  useEffect(() => {
    if (!dirty) {
      setDraft(fromRecord);
      setBase(key);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  const set = <K extends keyof T>(k: K, v: T[K]) => setDraft((d) => ({ ...d, [k]: v }));
  const reset = () => {
    setDraft(fromRecord);
    setBase(key);
  };
  return { draft, setDraft, set, dirty, reset };
}

function useSave(onUpdated: () => void) {
  const { t } = useI18n();
  const { toast } = useToast();
  const [saving, setSaving] = useState(false);
  const save = async (fn: () => Promise<unknown>, after?: () => void) => {
    setSaving(true);
    try {
      await fn();
      toast(t('metadata_updated'), 'success');
      after?.();
      onUpdated();
    } catch (e: any) {
      toast(e.message || 'Save failed', 'error');
    } finally {
      setSaving(false);
    }
  };
  return { saving, save };
}

const BasicsSection: React.FC<{ p: Printer; onUpdated: () => void }> = ({ p, onUpdated }) => {
  const { t } = useI18n();
  const f = useSectionForm({
    name: p.name || '',
    custom_label: p.custom_label || '',
    site: p.site || '',
    remark: p.remark || '',
    enabled: p.enabled,
  });
  const { saving, save } = useSave(onUpdated);
  const d = f.draft;
  return (
    <Section
      title={t('settings_basics')}
      where={t('settings_saved_in_cps')}
      dirty={f.dirty}
      saving={saving}
      onReset={f.reset}
      onSave={() =>
        save(() =>
          api.updatePrinter(p.printer_id, {
            name: d.name.trim(),
            custom_label: d.custom_label.trim(),
            site: d.site.trim(),
            remark: d.remark.trim(),
            enabled: d.enabled,
          }),
        )
      }
    >
      <div className="grid grid-cols-2 gap-2">
        <div className="form-group mb-0">
          <label>{t('th_display_name')}</label>
          <input type="text" value={d.name} onChange={(e) => f.set('name', e.target.value)} />
        </div>
        <div className="form-group mb-0">
          <label>{t('custom_label')}</label>
          <input
            type="text"
            value={d.custom_label}
            onChange={(e) => f.set('custom_label', e.target.value)}
            placeholder={t('custom_label_placeholder')}
          />
        </div>
      </div>
      <div className="form-group mb-0">
        <label>{t('detail_site')}</label>
        <input
          type="text"
          value={d.site}
          onChange={(e) => f.set('site', e.target.value)}
          placeholder="e.g. wh1"
        />
      </div>
      <div className="form-group mb-0">
        <label>{t('remark')}</label>
        <textarea
          rows={2}
          value={d.remark}
          onChange={(e) => f.set('remark', e.target.value)}
          placeholder={t('remark_placeholder')}
        />
      </div>
      <label className="flex items-center gap-2 cursor-pointer text-sm font-medium">
        <input
          type="checkbox"
          checked={!!d.enabled}
          onChange={(e) => f.set('enabled', e.target.checked)}
        />
        {t('printer_enabled')}
      </label>
    </Section>
  );
};

// Paper sizes like "4x6", "100x150mm" or "60*40" fill in W/H.
function parsePaperSize(val: string): [number, number] | null {
  const c = val.toLowerCase().trim();
  const inches: Record<string, [number, number]> = {
    '4x6': [101.6, 152.4],
    '4x6in': [101.6, 152.4],
    '4x2': [101.6, 50.8],
    '4x2in': [101.6, 50.8],
    '2x1': [50.8, 25.4],
    '2x1in': [50.8, 25.4],
  };
  if (inches[c]) return inches[c];
  const m = c.match(/^(\d+(?:\.\d+)?)\s*[x*]\s*(\d+(?:\.\d+)?)\s*(mm)?$/);
  return m ? [parseFloat(m[1]), parseFloat(m[2])] : null;
}

const MediaSection: React.FC<{ p: Printer; onUpdated: () => void }> = ({ p, onUpdated }) => {
  const { t } = useI18n();
  const f = useSectionForm({
    paper_size: p.paper_size || '',
    media_width_mm: p.media_width_mm as number | undefined,
    media_height_mm: p.media_height_mm as number | undefined,
    dpi: p.dpi as number | undefined,
    dpmm: p.dpmm as number | undefined,
    orientation: p.orientation || '',
    print_direction: p.print_direction || '',
  });
  const { saving, save } = useSave(onUpdated);
  const d = f.draft;
  const onPaper = (val: string) => {
    const size = parsePaperSize(val);
    f.setDraft((x) => ({
      ...x,
      paper_size: val,
      ...(size ? { media_width_mm: size[0], media_height_mm: size[1] } : {}),
    }));
  };
  const onDPI = (dpi: number) => {
    const dpmm =
      ({ 203: 8, 300: 12, 600: 24 } as Record<number, number>)[dpi] ??
      Math.round((dpi / 25.4) * 10) / 10;
    f.setDraft((x) => ({ ...x, dpi, dpmm }));
  };
  return (
    <Section
      title={t('settings_media')}
      where={t('settings_saved_in_cps')}
      dirty={f.dirty}
      saving={saving}
      onReset={f.reset}
      onSave={() =>
        save(() =>
          api.updatePrinter(p.printer_id, {
            paper_size: d.paper_size.trim(),
            media_width_mm: d.media_width_mm,
            media_height_mm: d.media_height_mm,
            dpi: d.dpi,
            dpmm: d.dpmm,
            orientation: d.orientation,
            media_orientation: d.orientation,
            print_direction: d.print_direction,
          }),
        )
      }
    >
      <div className="grid grid-cols-3 gap-2">
        <div className="form-group mb-0">
          <label>{t('paper_size')}</label>
          <input
            type="text"
            value={d.paper_size}
            onChange={(e) => onPaper(e.target.value)}
            placeholder="e.g. 60x40mm, 4x6"
          />
        </div>
        <div className="form-group mb-0">
          <label>W (mm)</label>
          <input
            type="number"
            step="0.1"
            value={d.media_width_mm ?? ''}
            onChange={(e) => f.set('media_width_mm', parseFloat(e.target.value) || undefined)}
          />
        </div>
        <div className="form-group mb-0">
          <label>H (mm)</label>
          <input
            type="number"
            step="0.1"
            value={d.media_height_mm ?? ''}
            onChange={(e) => f.set('media_height_mm', parseFloat(e.target.value) || undefined)}
          />
        </div>
      </div>
      <div className="grid grid-cols-2 gap-2">
        <div className="form-group mb-0">
          <label>{t('dpi_label')}</label>
          <select value={d.dpi ?? 203} onChange={(e) => onDPI(parseInt(e.target.value))}>
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
            value={d.dpmm ?? 8}
            onChange={(e) => f.set('dpmm', parseFloat(e.target.value) || undefined)}
          />
        </div>
      </div>
      <div className="grid grid-cols-2 gap-2">
        <div className="form-group mb-0">
          <label>{t('orientation_label')}</label>
          <select value={d.orientation} onChange={(e) => f.set('orientation', e.target.value)}>
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
            value={d.print_direction}
            onChange={(e) => f.set('print_direction', e.target.value)}
          >
            <option value="">{t('not_set')}</option>
            <option value="top_first">{t('direction_top_first')}</option>
            <option value="bottom_first">{t('direction_bottom_first')}</option>
          </select>
        </div>
      </div>
    </Section>
  );
};

// Offsets and calibration live on the printer, not in CPS: saving writes them to the
// device for every label it prints.
const PrinterSideSection: React.FC<{
  p: Printer;
  diag: ReturnType<typeof useDiagnostics>;
  onTestPrint: () => void;
}> = ({ p, diag, onTestPrint }) => {
  const { t } = useI18n();
  const { toast } = useToast();
  const c = diag.result?.config;
  const f = useSectionForm({ top: c?.label_top_dots ?? 0, left: c?.left_position_dots ?? 0 });
  const [saving, setSaving] = useState(false);
  const [calibrateArmed, setCalibrateArmed] = useState(false);
  const d = f.draft;

  const saveToPrinter = async () => {
    setSaving(true);
    try {
      if (d.top !== (c?.label_top_dots ?? 0))
        await api.maintenance(p.printer_id, 'label_top', d.top);
      if (d.left !== (c?.left_position_dots ?? 0))
        await api.maintenance(p.printer_id, 'left_shift', d.left);
      toast(t('diag_offsets_saved'), 'success');
      f.reset();
      await diag.run(); // read the saved values back from the printer
    } catch (e: any) {
      toast(e.message || 'Saving offsets failed', 'error');
    } finally {
      setSaving(false);
    }
  };

  const calibrate = async () => {
    if (!calibrateArmed) {
      setCalibrateArmed(true);
      return;
    }
    setCalibrateArmed(false);
    try {
      const res = await api.maintenance(p.printer_id, 'calibrate');
      toast(t('diag_sent', { cmd: res.sent, note: res.note }), 'success');
      void diag.run();
    } catch (e: any) {
      toast(e.message || 'Calibration failed', 'error');
    }
  };

  return (
    <>
      <Section
        title={t('settings_printer_side')}
        where={t('settings_saved_on_printer')}
        dirty={!!c && f.dirty}
        saving={saving}
        saveLabel={t('settings_save_to_printer')}
        onReset={f.reset}
        onSave={saveToPrinter}
        extraActions={
          <button type="button" className="btn btn-secondary btn-sm" onClick={onTestPrint}>
            <Play size={13} /> {t('btn_print_alignment_label')}
          </button>
        }
      >
        {!c && (
          <p className="text-xs text-muted">
            {diag.running ? t('diag_running') : t('settings_printer_unreadable')}
          </p>
        )}
        <div className="grid grid-cols-2 gap-2">
          <div className="form-group mb-0">
            <label htmlFor="offset-top">{t('diag_label_top_field')}</label>
            <input
              id="offset-top"
              type="number"
              min={-120}
              max={120}
              disabled={!c}
              value={d.top}
              onChange={(e) => f.set('top', Number(e.target.value))}
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
              disabled={!c}
              value={d.left}
              onChange={(e) => f.set('left', Number(e.target.value))}
            />
            <span className="text-xs text-muted">{t('diag_left_shift_hint')}</span>
          </div>
        </div>
        <p className="text-xs text-muted flex gap-1">
          <Info size={13} style={{ flexShrink: 0 }} /> {t('diag_offsets_note')}
        </p>
      </Section>

      <div className="adjust-card flex items-center justify-between gap-2 flex-wrap">
        <div>
          <div className="section-title">{t('diag_calibrate')}</div>
          <p className="text-xs text-muted">{t('diag_calibrate_note')}</p>
        </div>
        <button
          type="button"
          className={`btn btn-sm ${calibrateArmed ? 'btn-danger' : 'btn-secondary'}`}
          onClick={calibrate}
        >
          <Crosshair size={13} />{' '}
          {calibrateArmed ? t('diag_calibrate_confirm') : t('diag_calibrate')}
        </button>
      </div>
    </>
  );
};
