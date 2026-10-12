import React from 'react';
import { useI18n } from '../i18n/context';
import { PrinterSummary } from '../types';
import { Tag } from 'lucide-react';

interface RollsStripProps {
  printers: PrinterSummary[];
}

export const RollsStrip: React.FC<RollsStripProps> = ({ printers }) => {
  const { t } = useI18n();

  return (
    <section className="rolls-strip">
      <div className="strip-container">
        <div className="strip-header">
          <span className="strip-label">{t('strip_label')}</span>
          <span className="strip-sub">{t('strip_sub')}</span>
        </div>

        <div className="rolls-grid">
          {printers.length > 0 ? (
            printers.map((p) => {
              const rollClass =
                p.paper_size === '4x6'
                  ? 'roll-4x6'
                  : p.paper_size === '4x2'
                  ? 'roll-4x2'
                  : 'roll-2x1';

              return (
                <div
                  key={p.printer_id}
                  className={`roll-card-item ${!p.usable ? 'unusable' : ''}`}
                >
                  {/* Row 1: Printer ID on left, Status Badge on right */}
                  <div className="roll-card-top">
                    <span className="roll-card-id">{p.printer_id}</span>
                    <span className={`badge-pill ${p.usable ? 'badge-ok' : 'badge-danger'}`}>
                      <span className={`status-dot ${p.usable ? 'ok' : 'bad'}`} />
                      <span>{p.usable ? t('usable') : t('offline')}</span>
                    </span>
                  </div>

                  {/* Row 2: Custom Label (if any) on left, Roll Size Badge on right */}
                  <div className="roll-card-middle">
                    <div className="roll-card-tag-wrap">
                      {p.custom_label ? (
                        <span className="badge-pill badge-custom-label">
                          <Tag size={9} /> {p.custom_label}
                        </span>
                      ) : (
                        <span className="text-xs text-muted font-mono">{p.model || 'Zebra'}</span>
                      )}
                    </div>
                    <span className={`badge-roll ${rollClass}`}>
                      {p.paper_size || 'any'}
                    </span>
                  </div>

                  {/* Row 3: Specs & Dimensions */}
                  <div className="roll-card-specs">
                    <span>{p.site || '—'}</span>
                    <span className="sep">·</span>
                    <span>{p.model || 'Zebra'}</span>
                    {p.media_width_mm && p.media_height_mm ? (
                      <>
                        <span className="sep">·</span>
                        <span className="font-mono">{p.media_width_mm}×{p.media_height_mm}mm</span>
                      </>
                    ) : null}
                  </div>

                  {/* Row 4: Optional Remark */}
                  {p.remark && (
                    <div className="roll-card-remark" title={p.remark}>
                      {p.remark}
                    </div>
                  )}
                </div>
              );
            })
          ) : (
            <div className="text-muted text-sm py-2">{t('no_printers_registered')}</div>
          )}
        </div>
      </div>
    </section>
  );
};
