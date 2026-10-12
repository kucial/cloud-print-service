import React from 'react';
import { useI18n } from '../i18n/context';
import { Order, Line, PrinterSummary } from '../types';
import { Play, RotateCw, AlertTriangle, Eye, CheckCircle2, Box, Layers, Tag } from 'lucide-react';

interface OrderCardProps {
  order: Order;
  printers: PrinterSummary[];
  releasing: boolean;
  resending: boolean;
  onRelease: (id: string) => void;
  onResend: (id: string) => void;
  onSelectPrinter: (orderId: string, lineNo: number, printerId: string) => void;
  onPreview: (orderId: string, lineNo: number, page?: number) => void;
}

export const OrderCard: React.FC<OrderCardProps> = ({
  order: o,
  printers,
  releasing,
  resending,
  onRelease,
  onResend,
  onSelectPrinter,
  onPreview,
}) => {
  const { t } = useI18n();

  return (
    <div className="order-card">
      <div className="order-top">
        <div className="flex items-center gap-2 flex-wrap">
          <span className="order-id-badge">{o.id}</span>
          <span className="order-customer font-medium">{o.customer}</span>
          <span className="text-muted text-xs font-mono">
            SKU: {o.sku} ×{o.qty} · Bin {o.bin}
          </span>
        </div>

        <div className="order-actions">
          <button
            className="btn btn-sm btn-primary"
            onClick={() => onRelease(o.id)}
            disabled={releasing || resending}
          >
            <Play size={13} className={releasing ? 'spinning' : ''} />
            <span>{releasing ? t('releasing') : t('release_wave')}</span>
          </button>

          <button
            className="btn btn-sm btn-secondary"
            onClick={() => onResend(o.id)}
            disabled={releasing || resending}
            title={t('resend_wave')}
          >
            <RotateCw size={13} className={resending ? 'spinning' : ''} />
            <span>{resending ? t('resending') : t('resend_wave')}</span>
          </button>
        </div>
      </div>

      {/* Multi-spec package lines */}
      <div className="lines-container">
        {o.lines.map((line) => (
          <LineItemRow
            key={line.no}
            orderId={o.id}
            line={line}
            printers={printers}
            onSelectPrinter={onSelectPrinter}
            onPreview={onPreview}
          />
        ))}
      </div>
    </div>
  );
};

interface LineItemRowProps {
  orderId: string;
  line: Line;
  printers: PrinterSummary[];
  onSelectPrinter: (orderId: string, lineNo: number, printerId: string) => void;
  onPreview: (orderId: string, lineNo: number, page?: number) => void;
}

const LineItemRow: React.FC<LineItemRowProps> = ({
  orderId,
  line: l,
  printers,
  onSelectPrinter,
  onPreview,
}) => {
  const { t } = useI18n();
  const s = l.status;

  const rollClass =
    l.paper_size === '4x6' ? 'roll-4x6' : l.paper_size === '4x2' ? 'roll-4x2' : 'roll-2x1';

  const icon =
    l.paper_size === '4x6' ? (
      <Box size={16} className="text-accent" />
    ) : l.paper_size === '4x2' ? (
      <Layers size={16} className="text-warn" />
    ) : (
      <Tag size={16} className="text-purple" />
    );

  const assigned = printers.find(
    (p) => p.printer_id === (l.selected_printer || l.target_printer)
  );
  const isMismatch = assigned && assigned.paper_size && assigned.paper_size !== l.paper_size;

  return (
    <div className="line-item-row">
      <div className="line-spec-badge">
        <span className="line-icon">{icon}</span>
        <div>
          <div className="line-spec-title flex items-center gap-1.5 flex-wrap">
            <span>{l.label_name}</span>
            {l.copies && l.copies > 1 && (
              <span className="badge-pill badge-primary text-xs" style={{ fontSize: '10px', padding: '1px 5px' }}>
                ×{l.copies} labels
              </span>
            )}
          </div>
          <div className="line-spec-size font-mono">
            {l.width_mm} × {l.height_mm} mm
          </div>
          {l.pack_breakdown && (
            <div className="text-xs font-mono text-accent mt-0.5 flex items-center gap-1" style={{ fontSize: '11px' }}>
              <span>📦</span> {l.pack_breakdown}
            </div>
          )}
        </div>
        <span className={`badge-roll ${rollClass}`}>{l.paper_size}</span>
      </div>

      <div className="line-routing">
        <span className="text-xs text-muted whitespace-nowrap">{t('roll_printer_label')}</span>
        <select
          className="printer-select"
          value={l.selected_printer || l.target_printer || ''}
          onChange={(e) => onSelectPrinter(orderId, l.no, e.target.value)}
        >
          {printers.length > 0 ? (
            printers.map((p) => {
              const isMatched = p.paper_size === l.paper_size;
              return (
                <option key={p.printer_id} value={p.printer_id}>
                  {p.printer_id} [{p.paper_size || 'any'}]{!p.usable ? ' (offline)' : ''}{' '}
                  {isMatched ? '★' : ''}
                </option>
              );
            })
          ) : (
            <option value="">{t('no_compatible_printer')}</option>
          )}
        </select>

        {isMismatch && (
          <span className="badge-pill badge-danger text-xs" title={t('size_mismatch_title')}>
            <AlertTriangle size={11} /> {t('size_mismatch')}
          </span>
        )}
      </div>

      <div className="line-status-wrap">
        {l.page_copies && l.page_copies.length > 1 ? (
          <div className="flex items-center gap-1">
            <button
              className="btn btn-xs btn-secondary"
              onClick={() => onPreview(orderId, l.no, 1)}
              title="Preview Standard Full Box Format (Page 1)"
            >
              <Eye size={12} /> P1 (×{l.page_copies[0]})
            </button>
            <button
              className="btn btn-xs btn-secondary"
              onClick={() => onPreview(orderId, l.no, 2)}
              title="Preview Tail Remainder Box Format (Page 2)"
            >
              <Eye size={12} /> P2 {l.page_copies[1] > 0 ? `(Tail ×${l.page_copies[1]})` : '(Omitted)'}
            </button>
          </div>
        ) : (
          <button
            className="btn btn-xs btn-secondary"
            onClick={() => onPreview(orderId, l.no)}
            title="Preview rendered label at true aspect ratio"
          >
            <Eye size={12} /> {t('preview_btn')}
          </button>
        )}

        <span className={`state-badge s-${s?.state || 'queued'}`}>
          {s?.state ? (t(`state_${s.state}` as any) || s.state) : t('not_released')}
        </span>

        {s?.replayed && (
          <span className="badge-pill badge-ok">
            <CheckCircle2 size={11} /> {t('replayed')}
          </span>
        )}

        {s && s.attempts > 1 && (
          <span className="badge-pill text-xs">
            {t('attempts', { count: s.attempts })}
          </span>
        )}
      </div>
    </div>
  );
};
