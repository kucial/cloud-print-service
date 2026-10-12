import React from 'react';
import { useI18n } from '../i18n/context';
import { PrinterSummary } from '../types';
import { Printer as PrinterIcon, Tag } from 'lucide-react';

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
                  className={`roll-printer-card ${!p.usable ? 'unusable' : ''}`}
                >
                  <div className="roll-info">
                    <div className="flex items-center gap-1.5">
                      <span className="roll-printer-id">{p.printer_id}</span>
                      {p.custom_label && (
                        <span className="badge-pill badge-custom-label text-xs">
                          <Tag size={9} /> {p.custom_label}
                        </span>
                      )}
                    </div>
                    <div className="roll-printer-meta">
                      {p.site || 'no-site'} · {p.model || 'Zebra'}
                      {p.media_width_mm && p.media_height_mm
                        ? ` (${p.media_width_mm}×${p.media_height_mm}mm)`
                        : ''}
                    </div>
                  </div>

                  <div className="flex flex-col items-end gap-1">
                    <span className={`badge-roll ${rollClass}`}>
                      {p.paper_size || 'any'}
                    </span>
                    <span className={`text-xs ${p.usable ? 'text-ok' : 'text-bad'}`}>
                      {p.usable ? t('usable') : t('offline')}
                    </span>
                  </div>
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
