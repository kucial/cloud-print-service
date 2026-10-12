import React from 'react';
import { useI18n } from '../i18n/context';
import { Box, Layers, Tag, Eye, Download, AlertCircle } from 'lucide-react';

interface SidebarProps {
  previewUrl: string | null;
  previewDim: string | null;
  previewError: string | null;
  previewLoading: boolean;
  eventsLog: string[];
}

export const Sidebar: React.FC<SidebarProps> = ({
  previewUrl,
  previewDim,
  previewError,
  previewLoading,
  eventsLog,
}) => {
  const { t } = useI18n();

  return (
    <aside className="sidebar-section">
      {/* Architecture Guide */}
      <div className="card ios-card">
        <h3>{t('arch_title')}</h3>
        <p className="text-muted text-xs leading-relaxed mb-3">
          {t('arch_desc')}
        </p>

        <div className="roll-types-list">
          <div className="roll-type-item">
            <span className="badge-roll roll-4x6">4"×6"</span>
            <div>
              <div className="font-medium text-xs">{t('roll_4x6_title')}</div>
              <div className="text-xs text-muted">{t('roll_4x6_desc')}</div>
            </div>
          </div>

          <div className="roll-type-item">
            <span className="badge-roll roll-4x2">4"×2"</span>
            <div>
              <div className="font-medium text-xs">{t('roll_4x2_title')}</div>
              <div className="text-xs text-muted">{t('roll_4x2_desc')}</div>
            </div>
          </div>

          <div className="roll-type-item">
            <span className="badge-roll roll-2x1">2"×1"</span>
            <div>
              <div className="font-medium text-xs">{t('roll_2x1_title')}</div>
              <div className="text-xs text-muted">{t('roll_2x1_desc')}</div>
            </div>
          </div>
        </div>

        <p className="text-muted text-xs mt-3 mb-0 border-t border-line pt-2">
          {t('arch_idempotency_rule')}
        </p>
      </div>

      {/* Label Preview Card */}
      <div className="card ios-card">
        <div className="flex items-center justify-between mb-2">
          <h3>{t('preview_title')}</h3>
          {previewDim && <span className="text-xs font-mono text-muted">{previewDim}</span>}
        </div>

        <div className="preview-viewport">
          {previewLoading ? (
            <div className="text-muted text-xs p-4">{t('preview_rendering')}</div>
          ) : previewError ? (
            <div className="text-bad text-xs flex items-center gap-1.5 p-3">
              <AlertCircle size={16} />
              <span>{t('preview_error', { error: previewError })}</span>
            </div>
          ) : previewUrl ? (
            <div className="preview-img-container flex flex-col items-center">
              <img src={previewUrl} alt="Rendered label preview" />
              <a
                href={previewUrl}
                download="label_preview.png"
                className="btn btn-xs btn-secondary mt-2"
              >
                <Download size={11} /> Download
              </a>
            </div>
          ) : (
            <div className="text-muted text-xs p-4 flex flex-col items-center gap-2">
              <Eye size={24} className="opacity-40" />
              <span>{t('preview_empty_prompt')}</span>
            </div>
          )}
        </div>
      </div>

      {/* Live Dispatch Stream */}
      <div className="card ios-card">
        <div className="flex items-center justify-between mb-2">
          <h3>{t('events_title')}</h3>
          <span className="pulse-dot" />
        </div>
        <pre className="events-box font-mono">
          {eventsLog.length > 0 ? eventsLog.join('\n') : t('events_ready')}
        </pre>
      </div>
    </aside>
  );
};
