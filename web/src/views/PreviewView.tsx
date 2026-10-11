import React, { useState } from 'react';
import { api } from '../api';
import { useI18n } from '../i18n/context';
import { RenderPreviewMeta } from '../types';
import { useToast } from '../components/Toast';
import { Eye, Download, Play, AlertCircle } from 'lucide-react';

const SAMPLE_ZPLS = {
  standard: `^XA^CI28^PW812
^FO40,30^A0N,44,44^FDOUTBOUND WH1^FS
^FO40,90^GB732,3,3^FS
^FO40,110^A0N,30,30^FDOrder OK-88213   Bin A-03^FS
^FO40,150^A0N,26,26^FDSKU 12345 x2   Bay 7^FS
^FO40,190^BCN,90,Y,N,N^FD1234567890123^FS
^FO560,190^BQN,2,6^FDSKU12345^FS
^FO560,380^GB180,90,3^FS
^FO575,410^A0N,30,30^FDCHECKED^FS
^XZ`,
  carrierUps: `^XA
^LRN^MNY^MFN,N^LH10,12^MCY^PW812^CI27
^FO40,30^GB732,3,3^FS
^FO40,50^A0N,44,44^FDMERCHANT AB^FS
^FO40,110^A0N,30,30^FDOrder OK-88213   Bin A-03^FS
^FO40,150^A0N,26,26^FDSKU 12345 x2   Bay 7^FS
^FO40,200^BCN,90,Y,N,N^FD1Z680RA4DL08720000^FS
^FO560,200^BQN,2,6^FDSKU12345^FS
^FO560,380^GB180,90,3^FS
^FO575,410^A0N,30,30^FDCHECKED^FS
^XZ`,
  pallet: `^XA^CI28^PW812^LL1218
^FO40,40^GB732,1138,4^FS
^FO60,70^A0N,60,60^FDPALLET TAG^FS
^FO60,150^A0N,32,32^FDDESTINATION: BERLIN HUB 4^FS
^FO60,200^A0N,32,32^FDWEIGHT: 480 KG (48 CTN)^FS
^FO60,270^BY4,3,2^BCN,140,Y,N,N^FDSSCC001234567890123456^FS
^FO60,490^BQN,2,10^FDSSCC:001234567890123456^FS
^FO340,510^A0N,40,40^FDFRAGILE^FS
^FO340,570^A0N,30,30^FDDO NOT DOUBLE STACK^FS
^XZ`,
};

export const PreviewView: React.FC = () => {
  const { t } = useI18n();
  const { toast } = useToast();

  const [zpl, setZpl] = useState<string>(SAMPLE_ZPLS.standard);
  const [widthMm, setWidthMm] = useState<number>(101.6);
  const [heightMm, setHeightMm] = useState<number>(152.4);
  const [dpmm, setDpmm] = useState<number>(8);
  const [format, setFormat] = useState<string>('png');
  const [grayscale, setGrayscale] = useState<boolean>(false);
  const [upright, setUpright] = useState<boolean>(true);

  const [rendering, setRendering] = useState<boolean>(false);
  const [previewUrl, setPreviewUrl] = useState<string | null>(null);
  const [meta, setMeta] = useState<RenderPreviewMeta | null>(null);
  const [renderError, setRenderError] = useState<string | null>(null);

  const handleRender = async () => {
    if (!zpl.trim()) {
      toast(t('enter_zpl_prompt'), 'warning');
      return;
    }

    setRendering(true);
    setRenderError(null);
    try {
      const res = await api.renderPreview({
        zpl,
        width_mm: widthMm,
        height_mm: heightMm,
        dpmm,
        format,
        grayscale,
        upright,
      });

      if (previewUrl) {
        URL.revokeObjectURL(previewUrl);
      }

      const url = URL.createObjectURL(res.blob);
      setPreviewUrl(url);
      setMeta(res.meta);
    } catch (e: any) {
      setRenderError(e.message || 'Render failed');
      toast(e.message || 'Render failed', 'error');
    } finally {
      setRendering(false);
    }
  };

  const handleLoadSample = (key: keyof typeof SAMPLE_ZPLS) => {
    setZpl(SAMPLE_ZPLS[key]);
  };

  return (
    <div className="view-container">
      <div className="view-header">
        <div>
          <h1 className="view-title">{t('preview_title')}</h1>
          <p className="view-subtitle">{t('preview_subtitle')}</p>
        </div>

        <div className="header-actions">
          <div className="sample-selector">
            <span className="text-xs text-muted mr-2">{t('presets')}</span>
            <button className="btn btn-xs btn-secondary" onClick={() => handleLoadSample('standard')}>
              {t('preset_outbound')}
            </button>
            <button className="btn btn-xs btn-secondary ml-1" onClick={() => handleLoadSample('carrierUps')}>
              {t('preset_carrier')}
            </button>
            <button className="btn btn-xs btn-secondary ml-1" onClick={() => handleLoadSample('pallet')}>
              {t('preset_pallet')}
            </button>
          </div>
        </div>
      </div>

      <div className="split-view">
        {/* Editor Column */}
        <div className="editor-column">
          <div className="card h-full flex flex-col">
            <div className="card-header-bar mb-2">
              <span className="card-label">{t('card_zpl_code')}</span>
              <span className="text-xs text-muted font-mono">{t('bytes_count', { count: zpl.length })}</span>
            </div>

            <textarea
              className="zpl-textarea flex-1"
              value={zpl}
              onChange={(e) => setZpl(e.target.value)}
              placeholder={t('textarea_placeholder')}
              spellCheck={false}
            />

            {/* Geometry & Engine Options */}
            <div className="preview-controls-grid mt-4">
              <div className="form-group mb-0">
                <label>{t('width_mm')}</label>
                <input
                  type="number"
                  step="0.1"
                  value={widthMm}
                  onChange={(e) => setWidthMm(parseFloat(e.target.value) || 101.6)}
                />
              </div>

              <div className="form-group mb-0">
                <label>{t('height_mm')}</label>
                <input
                  type="number"
                  step="0.1"
                  value={heightMm}
                  onChange={(e) => setHeightMm(parseFloat(e.target.value) || 152.4)}
                />
              </div>

              <div className="form-group mb-0">
                <label>{t('dpmm_dpi')}</label>
                <select value={dpmm} onChange={(e) => setDpmm(parseInt(e.target.value, 10))}>
                  <option value={8}>8 dpmm (203 dpi)</option>
                  <option value={12}>12 dpmm (300 dpi)</option>
                  <option value={24}>24 dpmm (600 dpi)</option>
                  <option value={6}>6 dpmm (152 dpi)</option>
                </select>
              </div>

              <div className="form-group mb-0">
                <label>{t('format_label')}</label>
                <select value={format} onChange={(e) => setFormat(e.target.value)}>
                  <option value="png">PNG</option>
                  <option value="svg">SVG</option>
                  <option value="pdf">PDF</option>
                </select>
              </div>
            </div>

            <div className="flex items-center justify-between mt-4 pt-3 border-t border-line">
              <div className="flex items-center gap-4 text-xs text-muted">
                <label className="checkbox-label">
                  <input
                    type="checkbox"
                    checked={grayscale}
                    onChange={(e) => setGrayscale(e.target.checked)}
                  />
                  <span>{t('chk_grayscale')}</span>
                </label>
                <label className="checkbox-label">
                  <input
                    type="checkbox"
                    checked={upright}
                    onChange={(e) => setUpright(e.target.checked)}
                  />
                  <span>{t('chk_upright')}</span>
                </label>
              </div>

              <button
                className="btn btn-primary"
                onClick={handleRender}
                disabled={rendering}
              >
                <Play size={14} className={rendering ? 'spinning' : ''} />
                <span>{rendering ? t('btn_rendering') : t('btn_render_preview')}</span>
              </button>
            </div>
          </div>
        </div>

        {/* Output Preview Column */}
        <div className="preview-column">
          <div className="card h-full flex flex-col">
            <div className="card-header-bar mb-2">
              <span className="card-label">{t('card_rendered_output')}</span>
              {previewUrl && meta && (
                <a
                  className="btn btn-xs btn-secondary"
                  href={previewUrl}
                  download={`label_preview.${meta.format || 'png'}`}
                >
                  <Download size={12} /> {t('download')}
                </a>
              )}
            </div>

            {renderError ? (
              <div className="empty-box text-bad">
                <AlertCircle size={20} className="mb-2" />
                <div>{renderError}</div>
              </div>
            ) : previewUrl ? (
              <div className="preview-viewport flex-1 flex flex-col items-center justify-center">
                <div className="preview-img-container">
                  {meta?.format === 'svg' ? (
                    <object
                      data={previewUrl}
                      type="image/svg+xml"
                      className="preview-image-output"
                    >
                      SVG Preview
                    </object>
                  ) : (
                    <img
                      src={previewUrl}
                      alt="Rendered ZPL preview"
                      className="preview-image-output"
                    />
                  )}
                </div>

                {meta && (
                  <div className="meta-badges-row mt-3">
                    <span className="badge-pill font-mono">{meta.backend}</span>
                    <span className="badge-pill font-mono">
                      {meta.width_dots} × {meta.height_dots} dots
                    </span>
                    <span className="badge-pill font-mono text-ok">{meta.duration_ms}ms</span>
                    {meta.warnings && (
                      <span className="badge-pill badge-warn">{meta.warnings}</span>
                    )}
                  </div>
                )}
              </div>
            ) : (
              <div className="empty-box flex-1 flex flex-col items-center justify-center text-muted">
                <Eye size={32} className="opacity-40 mb-3" />
                <div>{t('preview_empty_prompt')}</div>
                <div className="text-xs opacity-60 mt-1">{t('preview_empty_sub')}</div>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
};
