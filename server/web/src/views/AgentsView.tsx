import React, { useState } from 'react';
import { useApp } from '../context/AppContext';
import { useI18n } from '../i18n/context';
import { api } from '../api';
import { useToast } from '../components/Toast';
import { AgentEnrollResponse } from '../types';
import { Trash2, RotateCw, CheckCircle2, AlertTriangle, Plus, KeyRound, X, Copy, Check, ShieldAlert } from 'lucide-react';

export const AgentsView: React.FC = () => {
  const { agents, refreshAgents } = useApp();
  const { t } = useI18n();
  const { toast } = useToast();
  const [loading, setLoading] = useState(false);
  const [enrolling, setEnrolling] = useState(false);

  const handleRemove = async (agentId: string) => {
    if (!confirm(t('confirm_remove_agent', { id: agentId }))) {
      return;
    }

    try {
      await api.removeAgent(agentId);
      await refreshAgents();
      toast(t('agent_removed', { id: agentId }), 'success');
    } catch (e: any) {
      toast(e.message || 'Failed to remove agent', 'error');
    }
  };

  const isHealthy = (lastSeen?: string) => {
    if (!lastSeen) return false;
    const diff = (Date.now() - new Date(lastSeen).getTime()) / 1000;
    return diff < 60; // seen within 60s
  };

  return (
    <div className="view-container">
      <div className="view-header">
        <div>
          <h1 className="view-title">{t('agents_title')}</h1>
          <p className="view-subtitle">{t('agents_subtitle')}</p>
        </div>

        <div className="header-actions">
          <button
            className="btn btn-secondary"
            onClick={async () => {
              setLoading(true);
              await refreshAgents();
              setLoading(false);
            }}
            disabled={loading}
          >
            <RotateCw size={14} className={loading ? 'spinning' : ''} />
            <span>{t('refresh')}</span>
          </button>
          <button className="btn btn-primary" onClick={() => setEnrolling(true)}>
            <Plus size={14} />
            <span>{t('btn_enroll_agent')}</span>
          </button>
        </div>
      </div>

      <div className="table-responsive">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t('th_agent_id')}</th>
              <th>{t('th_site')}</th>
              <th>{t('th_hostname')}</th>
              <th>{t('th_os')}</th>
              <th>{t('th_version')}</th>
              <th>{t('th_local_address')}</th>
              <th>{t('th_printers')}</th>
              <th>{t('status')}</th>
              <th>{t('th_last_seen')}</th>
              <th>{t('actions')}</th>
            </tr>
          </thead>
          <tbody>
            {agents.length > 0 ? (
              agents.map((a) => {
                const healthy = isHealthy(a.last_seen);
                return (
                  <tr key={a.id}>
                    <td className="font-mono font-medium">{a.id}</td>
                    <td>{a.site || '—'}</td>
                    <td className="font-mono text-sm text-muted">{a.hostname || '—'}</td>
                    <td className="font-mono text-sm text-muted">{a.os || '—'}</td>
                    <td className="font-mono text-sm">{a.version || 'dev'}</td>
                    <td className="font-mono text-sm">{a.agent_addr || '—'}</td>
                    <td>
                      <span className="badge-pill font-mono">{a.printers?.length || 0}</span>
                    </td>
                    <td>
                      {healthy ? (
                        <span className="badge-pill badge-ok">
                          <CheckCircle2 size={12} /> {t('agent_healthy')}
                        </span>
                      ) : (
                        <span className="badge-pill badge-danger">
                          <AlertTriangle size={12} /> {t('agent_stale')}
                        </span>
                      )}
                    </td>
                    <td className="text-muted text-sm">{formatAgo(a.last_seen)}</td>
                    <td>
                      <button
                        className="btn btn-xs btn-danger"
                        onClick={() => handleRemove(a.id)}
                        title={t('btn_remove')}
                      >
                        <Trash2 size={12} /> {t('btn_remove')}
                      </button>
                    </td>
                  </tr>
                );
              })
            ) : (
              <tr>
                <td colSpan={10} className="text-center text-muted py-6">
                  {t('no_agents')}
                  <br />
                  <code className="text-xs mt-2 inline-block">
                    cpsctl agent enroll --id pc-station-1 --site wh1
                  </code>
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {enrolling && <EnrollModal onClose={() => { setEnrolling(false); refreshAgents(); }} />}
    </div>
  );
};

// EnrollModal issues a one-time agent enrollment token (POST /admin/agents/enroll), the
// panel's equivalent of `cpsctl agent enroll`, and shows it once with ready-to-paste
// commands for each kind of host.
const EnrollModal: React.FC<{ onClose: () => void }> = ({ onClose }) => {
  const { t } = useI18n();
  const { toast } = useToast();
  const [agentId, setAgentId] = useState('');
  const [site, setSite] = useState('');
  const [ttl, setTtl] = useState('24h');
  const [server, setServer] = useState(window.location.origin);
  const [submitting, setSubmitting] = useState(false);
  const [issued, setIssued] = useState<AgentEnrollResponse | null>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!agentId.trim()) return;
    setSubmitting(true);
    try {
      const res = await api.enrollAgent({
        agent_id: agentId.trim(),
        site: site.trim() || undefined,
        ttl: ttl.trim() || undefined,
      });
      setIssued(res);
      toast(t('enroll_issued_toast', { id: res.agent_id }), 'success');
    } catch (err: any) {
      toast(err.message || 'Failed to create enrollment token', 'error');
    } finally {
      setSubmitting(false);
    }
  };

  const id = issued?.agent_id || '';
  const tok = issued?.token || '';
  const siteArg = site.trim() ? ` -site ${site.trim()}` : '';

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-card modal-lg" onClick={(e) => e.stopPropagation()}>
        <div className="modal-header">
          <div className="modal-title-wrap">
            <KeyRound size={18} className={issued ? 'text-ok' : 'text-accent'} />
            <h2>{issued ? t('enroll_done_title', { id }) : t('enroll_title')}</h2>
          </div>
          <button className="modal-close-btn" onClick={onClose}>
            <X size={16} />
          </button>
        </div>

        {!issued ? (
          <form onSubmit={handleSubmit}>
            <div className="modal-body">
              <p className="text-muted text-sm mb-4">{t('enroll_intro')}</p>
              <div className="form-group">
                <label>{t('th_agent_id')}</label>
                <input
                  type="text"
                  placeholder="pc7"
                  value={agentId}
                  onChange={(e) => setAgentId(e.target.value)}
                  required
                  autoFocus
                />
                <p className="text-muted text-xs mt-2">{t('enroll_agent_id_hint')}</p>
              </div>
              <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mt-3">
                <div className="form-group">
                  <label>{t('enroll_site_label')}</label>
                  <input type="text" placeholder="wh1" value={site} onChange={(e) => setSite(e.target.value)} />
                </div>
                <div className="form-group">
                  <label>{t('ttl_label')}</label>
                  <input type="text" placeholder="24h" value={ttl} onChange={(e) => setTtl(e.target.value)} />
                  <p className="text-muted text-xs mt-2">{t('enroll_ttl_hint')}</p>
                </div>
              </div>
            </div>
            <div className="modal-footer">
              <button type="button" className="btn btn-secondary" onClick={onClose}>
                {t('cancel')}
              </button>
              <button type="submit" className="btn btn-primary" disabled={submitting || !agentId.trim()}>
                <KeyRound size={14} /> {submitting ? t('btn_generating') : t('btn_create_enroll_token')}
              </button>
            </div>
          </form>
        ) : (
          <>
            <div className="modal-body">
              <div className="alert-box alert-warn mb-4">
                <ShieldAlert size={16} />
                <span>
                  <strong>
                    {t('enroll_one_time_warn', {
                      expires: issued.expires ? new Date(issued.expires).toLocaleString() : t('never'),
                    })}
                  </strong>
                </span>
              </div>

              <div className="form-group">
                <label>{t('enroll_token_label')}</label>
                <CopyBox text={tok} />
              </div>

              <div className="form-group mt-3">
                <label>{t('enroll_server_label')}</label>
                <input type="text" value={server} onChange={(e) => setServer(e.target.value)} />
                <p className="text-muted text-xs mt-2">{t('enroll_server_hint')}</p>
              </div>

              <p className="text-sm mt-3">{t('enroll_how_gui')}</p>
              <CommandBlock
                label={t('enroll_how_windows')}
                text={`set CPS_AGENT_SERVER=${server}\ncps-agent.exe -enroll ${tok} -id ${id}${siteArg} -install`}
              />
              <CommandBlock
                label={t('enroll_how_linux')}
                text={`CPS_AGENT_SERVER=${server} cps-agent -enroll ${tok} -id ${id}${siteArg} -config /etc/cps-agent/agent.json`}
              />
              <CommandBlock
                label={t('enroll_how_router')}
                text={`/jffs/cps-agent/cps-agent.sh enroll ${server} ${tok} ${site.trim() || '<site>'} ${id}`}
              />
            </div>
            <div className="modal-footer">
              <button className="btn btn-primary" onClick={onClose}>
                {t('btn_done')}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
};

const CopyBox: React.FC<{ text: string }> = ({ text }) => {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const handleCopy = () => {
    navigator.clipboard.writeText(text);
    setCopied(true);
    setTimeout(() => setCopied(false), 3000);
  };
  return (
    <div className="secret-box">
      <code style={{ whiteSpace: 'pre-wrap' }}>{text}</code>
      <button type="button" className="btn btn-secondary btn-copy" onClick={handleCopy}>
        {copied ? <Check size={14} className="text-ok" /> : <Copy size={14} />}
        <span>{copied ? t('copied') : t('copy')}</span>
      </button>
    </div>
  );
};

const CommandBlock: React.FC<{ label: string; text: string }> = ({ label, text }) => (
  <div className="form-group mt-3">
    <label>{label}</label>
    <CopyBox text={text} />
  </div>
);

function formatAgo(isoString?: string): string {
  if (!isoString) return 'never';
  const sec = Math.max(0, Math.round((Date.now() - new Date(isoString).getTime()) / 1000));
  if (sec < 60) return `${sec}s ago`;
  const min = Math.round(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr}h ago`;
  return `${Math.round(hr / 24)}d ago`;
}
