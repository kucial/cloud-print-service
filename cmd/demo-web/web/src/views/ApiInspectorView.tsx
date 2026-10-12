import React, { useState, useEffect } from 'react';
import { useI18n } from '../i18n/context';
import { api } from '../api';
import { APITrace } from '../types';
import {
  Code2,
  Copy,
  Check,
  Trash2,
  RotateCw,
  Send,
  ArrowRight,
  Clock,
  Layers,
  FileText,
  Activity,
} from 'lucide-react';

export const ApiInspectorView: React.FC = () => {
  const { t } = useI18n();

  const [traces, setTraces] = useState<APITrace[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [categoryFilter, setCategoryFilter] = useState<string>('all');
  const [loading, setLoading] = useState(false);
  const [copiedType, setCopiedType] = useState<string | null>(null);

  const fetchTraces = async () => {
    try {
      const res = await api.getTraces();
      setTraces(res.items || []);
      if (!selectedId && res.items?.length > 0) {
        setSelectedId(res.items[0].id);
      }
    } catch {
      // ignore
    }
  };

  useEffect(() => {
    fetchTraces();
    const interval = setInterval(fetchTraces, 3000);
    return () => clearInterval(interval);
  }, [selectedId]);

  const handleClear = async () => {
    try {
      await api.clearTraces();
      setTraces([]);
      setSelectedId(null);
    } catch {
      // ignore
    }
  };

  const handleSendSample = async (action: string) => {
    setLoading(true);
    try {
      await api.sendSample(action);
      await fetchTraces();
    } finally {
      setLoading(false);
    }
  };

  const handleCopy = (text: string, type: string) => {
    navigator.clipboard.writeText(text);
    setCopiedType(type);
    setTimeout(() => setCopiedType(null), 2500);
  };

  const filteredTraces = traces.filter((tr) => {
    if (categoryFilter === 'all') return true;
    return tr.category === categoryFilter;
  });

  const selectedTrace = traces.find((t) => t.id === selectedId) || filteredTraces[0];

  return (
    <div className="inspector-layout">
      {/* Quick API Triggers Bar */}
      <div className="inspector-triggers-bar ios-glass-sheet">
        <div className="flex items-center gap-2">
          <Code2 size={16} className="text-accent" />
          <span className="font-bold text-sm">{t('quick_triggers')}</span>
        </div>

        <div className="trigger-buttons-row">
          <button
            className="btn btn-xs btn-secondary"
            onClick={() => handleSendSample('fleet_status')}
            disabled={loading}
          >
            <Send size={11} /> {t('trigger_fleet')}
          </button>
          <button
            className="btn btn-xs btn-primary"
            onClick={() => handleSendSample('submit_4x6')}
            disabled={loading}
          >
            <Send size={11} /> {t('trigger_job')}
          </button>
          <button
            className="btn btn-xs btn-secondary"
            onClick={() => handleSendSample('preview')}
            disabled={loading}
          >
            <Send size={11} /> {t('trigger_preview')}
          </button>
        </div>

        <div className="spacer" />

        <button className="btn btn-xs btn-danger" onClick={handleClear} title={t('clear_history')}>
          <Trash2 size={12} /> {t('clear_history')}
        </button>
      </div>

      <div className="inspector-panes">
        {/* Left Master List */}
        <div className="inspector-list-pane card">
          <div className="flex items-center justify-between mb-3">
            <span className="text-xs font-bold uppercase tracking-wider text-muted">
              {t('inspector_title')} ({filteredTraces.length})
            </span>
            <select
              className="text-xs py-1 px-2"
              value={categoryFilter}
              onChange={(e) => setCategoryFilter(e.target.value)}
            >
              <option value="all">{t('filter_all')}</option>
              <option value="dispatch">{t('filter_dispatch')}</option>
              <option value="fleet">{t('filter_fleet')}</option>
              <option value="preview">{t('filter_preview')}</option>
            </select>
          </div>

          <div className="traces-scroll-list">
            {filteredTraces.length > 0 ? (
              filteredTraces.map((tr) => {
                const isSelected = selectedTrace?.id === tr.id;
                const isOk = tr.status >= 200 && tr.status < 300;
                const isConflict = tr.status === 409;
                const isWarn = tr.status === 503;
                const statusColor = isOk ? 'badge-ok' : isConflict ? 'badge-warn' : isWarn ? 'badge-warn' : 'badge-danger';

                return (
                  <div
                    key={tr.id}
                    className={`trace-item-row ${isSelected ? 'active' : ''}`}
                    onClick={() => setSelectedId(tr.id)}
                  >
                    <div className="trace-item-head">
                      <span className={`method-badge method-${tr.method.toLowerCase()}`}>
                        {tr.method}
                      </span>
                      <span className="trace-path font-mono">{tr.path}</span>
                      <span className={`badge-pill ${statusColor} ml-auto font-mono text-xs`}>
                        {tr.status || 'ERR'}
                      </span>
                    </div>

                    <div className="trace-item-meta">
                      <span className="text-muted font-mono">{tr.latency_ms}ms</span>
                      <span className="sep">·</span>
                      <span className="text-muted">{new Date(tr.timestamp).toLocaleTimeString()}</span>
                      <span className="sep">·</span>
                      <span className="badge-pill text-xs">{tr.category}</span>
                    </div>
                  </div>
                );
              })
            ) : (
              <div className="empty-box py-8 text-xs">{t('no_traces')}</div>
            )}
          </div>
        </div>

        {/* Right Detail Pane */}
        <div className="inspector-detail-pane card">
          {selectedTrace ? (
            <div className="trace-detail-content">
              {/* Detail Header & Copy Actions */}
              <div className="trace-detail-header">
                <div>
                  <div className="flex items-center gap-2">
                    <span className={`method-badge method-${selectedTrace.method.toLowerCase()}`}>
                      {selectedTrace.method}
                    </span>
                    <span className="font-mono font-bold text-sm">{selectedTrace.path}</span>
                  </div>
                  <div className="text-xs text-muted font-mono mt-1">
                    {selectedTrace.url} · {selectedTrace.latency_ms}ms · {new Date(selectedTrace.timestamp).toLocaleString()}
                  </div>
                </div>

                <div className="copy-actions-row">
                  <button
                    className="btn btn-xs btn-secondary"
                    onClick={() => handleCopy(selectedTrace.curl_command, 'curl')}
                  >
                    {copiedType === 'curl' ? <Check size={11} className="text-ok" /> : <Copy size={11} />}
                    <span>{copiedType === 'curl' ? t('copied_curl') : t('copy_curl')}</span>
                  </button>

                  {selectedTrace.request_body && (
                    <button
                      className="btn btn-xs btn-secondary"
                      onClick={() => handleCopy(selectedTrace.request_body || '', 'req')}
                    >
                      {copiedType === 'req' ? <Check size={11} className="text-ok" /> : <Copy size={11} />}
                      <span>{copiedType === 'req' ? t('copied_req') : t('copy_req')}</span>
                    </button>
                  )}

                  {selectedTrace.response_body && (
                    <button
                      className="btn btn-xs btn-secondary"
                      onClick={() => handleCopy(selectedTrace.response_body || '', 'res')}
                    >
                      {copiedType === 'res' ? <Check size={11} className="text-ok" /> : <Copy size={11} />}
                      <span>{copiedType === 'res' ? t('copied_res') : t('copy_res')}</span>
                    </button>
                  )}
                </div>
              </div>

              {/* Request Box */}
              <div className="wire-section mt-4">
                <div className="wire-title">
                  <span>{t('section_request')}</span>
                  <span className="font-mono text-xs text-muted">
                    {selectedTrace.method} {selectedTrace.path} HTTP/1.1
                  </span>
                </div>

                <div className="headers-box mb-2">
                  <div className="box-sub-label">{t('request_headers')}</div>
                  {Object.entries(selectedTrace.request_headers || {}).map(([k, v]) => (
                    <div key={k} className="header-kv-row">
                      <span className="header-k font-mono">{k}:</span>
                      <span className="header-v font-mono">{v}</span>
                    </div>
                  ))}
                </div>

                {selectedTrace.request_body ? (
                  <div className="body-box">
                    <div className="box-sub-label">{t('request_body')}</div>
                    <pre className="payload-pre">{selectedTrace.request_body}</pre>
                  </div>
                ) : null}
              </div>

              {/* Response Box */}
              <div className="wire-section mt-4">
                <div className="wire-title">
                  <span>{t('section_response')}</span>
                  <span
                    className={`font-mono text-xs ${
                      selectedTrace.status >= 200 && selectedTrace.status < 300
                        ? 'text-ok'
                        : 'text-bad'
                    }`}
                  >
                    HTTP/1.1 {selectedTrace.status_text}
                  </span>
                </div>

                <div className="headers-box mb-2">
                  <div className="box-sub-label">{t('response_headers')}</div>
                  {Object.entries(selectedTrace.response_headers || {}).map(([k, v]) => (
                    <div key={k} className="header-kv-row">
                      <span className="header-k font-mono">{k}:</span>
                      <span className="header-v font-mono">{v}</span>
                    </div>
                  ))}
                </div>

                {selectedTrace.response_body ? (
                  <div className="body-box">
                    <div className="box-sub-label">{t('response_body')}</div>
                    <pre className="payload-pre">{selectedTrace.response_body}</pre>
                  </div>
                ) : null}
              </div>
            </div>
          ) : (
            <div className="empty-box py-16 flex flex-col items-center justify-center text-muted">
              <Code2 size={36} className="opacity-40 mb-3" />
              <div>{t('select_trace_prompt')}</div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
};
