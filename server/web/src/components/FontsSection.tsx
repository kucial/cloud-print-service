import React from 'react';
import { useI18n } from '../i18n/context';

type Font = { name: string; size: number; cjk?: boolean };

// The fonts the printer reports it stores (^HW). Business systems pick from this list
// (the same data is in the API as available_fonts), so it gets its own section: CJK
// fonts first and highlighted, then every other font with its size.
export const FontsSection: React.FC<{ fonts?: Font[] | null }> = ({ fonts }) => {
  const { t } = useI18n();
  const cjk = (fonts || []).filter((f) => f.cjk);
  const other = (fonts || []).filter((f) => !f.cjk);
  const kb = (n: number) =>
    n >= 1 << 20 ? `${(n / (1 << 20)).toFixed(1)} MB` : `${Math.max(1, Math.round(n / 1024))} KB`;
  return (
    <div className="adjust-card">
      <div className="flex items-center justify-between gap-2 flex-wrap">
        <div className="section-title">{t('available_fonts_label')}</div>
        <span className="text-xs text-muted">{t('fonts_source_hint')}</span>
      </div>
      {fonts == null ? (
        <p className="text-sm text-muted mt-2">{t('fonts_unknown')}</p>
      ) : fonts.length === 0 ? (
        <p className="text-sm text-muted mt-2">{t('diag_none')}</p>
      ) : (
        <>
          <div className="text-xs text-muted mt-2">{t('fonts_cjk_heading', { n: cjk.length })}</div>
          <div className="flex gap-2 flex-wrap mt-1">
            {cjk.length === 0 && <span className="text-sm text-muted">{t('diag_none')}</span>}
            {cjk.map((f) => (
              <span key={f.name} className="badge-pill badge-primary font-mono">
                中 {f.name} · {kb(f.size)}
              </span>
            ))}
          </div>
          <div className="text-xs text-muted mt-3">
            {t('fonts_other_heading', { n: other.length })}
          </div>
          <div className="fonts-grid mt-1">
            {other.map((f) => (
              <span key={f.name} className="font-mono text-xs">
                {f.name} <span className="text-muted">{kb(f.size)}</span>
              </span>
            ))}
          </div>
        </>
      )}
    </div>
  );
};
