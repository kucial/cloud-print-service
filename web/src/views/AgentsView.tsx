import React, { useState } from 'react';
import { useApp } from '../context/AppContext';
import { useI18n } from '../i18n/context';
import { api } from '../api';
import { useToast } from '../components/Toast';
import { Trash2, RotateCw, CheckCircle2, AlertTriangle } from 'lucide-react';

export const AgentsView: React.FC = () => {
  const { agents, refreshAgents } = useApp();
  const { t } = useI18n();
  const { toast } = useToast();
  const [loading, setLoading] = useState(false);

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
    </div>
  );
};

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
