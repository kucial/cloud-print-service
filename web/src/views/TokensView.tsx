import React, { useState, useEffect } from 'react';
import { api } from '../api';
import { useI18n } from '../i18n/context';
import { TokenItem, IssuedTokenResponse } from '../types';
import { useToast } from '../components/Toast';
import { Key, Plus, RotateCw, Trash2, Copy, Check, X, ShieldAlert } from 'lucide-react';

export const TokensView: React.FC = () => {
  const { t } = useI18n();
  const { toast } = useToast();
  const [tokens, setTokens] = useState<TokenItem[]>([]);
  const [loading, setLoading] = useState(false);

  // Form state
  const [clientId, setClientId] = useState('');
  const [role, setRole] = useState<'client' | 'admin'>('client');
  const [ttl, setTtl] = useState('90d');
  const [note, setNote] = useState('');
  const [submitting, setSubmitting] = useState(false);

  // Secret reveal modal
  const [revealedToken, setRevealedToken] = useState<IssuedTokenResponse | null>(null);

  const fetchTokens = async () => {
    setLoading(true);
    try {
      const res = await api.getTokens();
      setTokens(res.items || []);
    } catch (e: any) {
      toast(e.message || 'Failed to fetch tokens', 'error');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchTokens();
  }, []);

  const handleIssue = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!clientId.trim()) {
      toast(t('client_id_label') + ' required', 'warning');
      return;
    }

    setSubmitting(true);
    try {
      const res = await api.issueToken({
        client_id: clientId.trim(),
        role,
        ttl: ttl.trim() || undefined,
        note: note.trim() || undefined,
      });
      setRevealedToken(res);
      setClientId('');
      setNote('');
      await fetchTokens();
      toast(t('token_issued_toast', { client: res.client_id }), 'success');
    } catch (e: any) {
      toast(e.message || 'Failed to issue token', 'error');
    } finally {
      setSubmitting(false);
    }
  };

  const handleRotate = async (id: string) => {
    if (!confirm(t('confirm_rotate', { id }))) {
      return;
    }

    try {
      const res = await api.rotateToken(id);
      setRevealedToken(res);
      await fetchTokens();
      toast(t('token_rotated_toast'), 'success');
    } catch (e: any) {
      toast(e.message || 'Failed to rotate token', 'error');
    }
  };

  const handleRevoke = async (id: string) => {
    if (!confirm(t('confirm_revoke', { id }))) {
      return;
    }

    try {
      await api.revokeToken(id);
      await fetchTokens();
      toast(t('token_revoked_toast'), 'info');
    } catch (e: any) {
      toast(e.message || 'Failed to revoke token', 'error');
    }
  };

  return (
    <div className="view-container">
      <div className="view-header">
        <div>
          <h1 className="view-title">{t('tokens_title')}</h1>
          <p className="view-subtitle">{t('tokens_subtitle')}</p>
        </div>

        <div className="header-actions">
          <button className="btn btn-secondary" onClick={fetchTokens} disabled={loading}>
            <RotateCw size={14} className={loading ? 'spinning' : ''} />
            <span>{t('refresh')}</span>
          </button>
        </div>
      </div>

      {/* Issue Token Form Card */}
      <div className="card mb-6">
        <h2 className="card-title mb-4">
          <Key size={16} className="text-accent" /> {t('issue_card_title')}
        </h2>
        <form onSubmit={handleIssue}>
          <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
            <div className="form-group">
              <label>{t('client_id_label')}</label>
              <input
                type="text"
                placeholder="client_id"
                value={clientId}
                onChange={(e) => setClientId(e.target.value)}
                required
              />
            </div>

            <div className="form-group">
              <label>{t('role_label')}</label>
              <select value={role} onChange={(e) => setRole(e.target.value as any)}>
                <option value="client">{t('role_client')}</option>
                <option value="admin">{t('role_admin')}</option>
              </select>
            </div>

            <div className="form-group">
              <label>{t('ttl_label')}</label>
              <input
                type="text"
                placeholder={t('ttl_placeholder')}
                value={ttl}
                onChange={(e) => setTtl(e.target.value)}
              />
            </div>

            <div className="form-group">
              <label>{t('note_label')}</label>
              <input
                type="text"
                placeholder={t('note_placeholder')}
                value={note}
                onChange={(e) => setNote(e.target.value)}
              />
            </div>
          </div>

          <div className="flex items-center justify-between mt-4">
            <p className="text-muted text-xs">{t('token_hash_notice')}</p>
            <button type="submit" className="btn btn-primary" disabled={submitting}>
              <Plus size={14} /> {submitting ? t('btn_generating') : t('btn_issue_token')}
            </button>
          </div>
        </form>
      </div>

      {/* Tokens Table */}
      <div className="table-responsive">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t('th_token_id')}</th>
              <th>{t('th_client')}</th>
              <th>{t('role_label')}</th>
              <th>{t('th_prefix')}</th>
              <th>{t('th_note')}</th>
              <th>{t('th_expires_at')}</th>
              <th>{t('status')}</th>
              <th>{t('th_uses')}</th>
              <th>{t('actions')}</th>
            </tr>
          </thead>
          <tbody>
            {tokens.length > 0 ? (
              tokens.map((tItem) => {
                const isRevoked = !!tItem.revoked_at;
                const isExpired = tItem.expires_at ? new Date(tItem.expires_at) < new Date() : false;
                const status = isRevoked ? 'revoked' : isExpired ? 'expired' : 'active';
                const statusLabel =
                  status === 'active'
                    ? t('status_active')
                    : status === 'revoked'
                    ? t('status_revoked')
                    : t('status_expired');

                return (
                  <tr key={tItem.id}>
                    <td className="font-mono font-medium">{tItem.id}</td>
                    <td className="font-mono">{tItem.client_id}</td>
                    <td>
                      <span className={`badge-pill ${tItem.role === 'admin' ? 'badge-warn' : ''}`}>
                        {tItem.role}
                      </span>
                    </td>
                    <td className="font-mono text-muted text-xs">cps_live_{tItem.prefix}…</td>
                    <td className="text-muted text-sm">{tItem.note || '—'}</td>
                    <td className="text-muted text-sm font-mono">
                      {tItem.expires_at ? new Date(tItem.expires_at).toLocaleDateString() : t('never')}
                    </td>
                    <td>
                      <span
                        className={`badge-pill ${
                          status === 'active'
                            ? 'badge-ok'
                            : status === 'revoked'
                            ? 'badge-danger'
                            : 'badge-warn'
                        }`}
                      >
                        {statusLabel}
                      </span>
                    </td>
                    <td className="font-mono text-sm">{tItem.uses || 0}</td>
                    <td>
                      {!isRevoked && (
                        <div className="table-btn-group">
                          <button
                            className="btn btn-xs btn-secondary"
                            onClick={() => handleRotate(tItem.id)}
                            title={t('btn_rotate')}
                          >
                            <RotateCw size={11} /> {t('btn_rotate')}
                          </button>
                          <button
                            className="btn btn-xs btn-danger"
                            onClick={() => handleRevoke(tItem.id)}
                            title={t('btn_revoke')}
                          >
                            <Trash2 size={11} /> {t('btn_revoke')}
                          </button>
                        </div>
                      )}
                    </td>
                  </tr>
                );
              })
            ) : (
              <tr>
                <td colSpan={9} className="text-center text-muted py-6">
                  {t('no_tokens')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {/* Secret Reveal Modal */}
      {revealedToken && (
        <SecretModal tokenInfo={revealedToken} onClose={() => setRevealedToken(null)} />
      )}
    </div>
  );
};

const SecretModal: React.FC<{ tokenInfo: IssuedTokenResponse; onClose: () => void }> = ({
  tokenInfo,
  onClose,
}) => {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);

  const handleCopy = () => {
    navigator.clipboard.writeText(tokenInfo.token);
    setCopied(true);
    setTimeout(() => setCopied(false), 3000);
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-card modal-lg" onClick={(e) => e.stopPropagation()}>
        <div className="modal-header">
          <div className="modal-title-wrap">
            <Key size={18} className="text-ok" />
            <h2>{t('secret_modal_title', { client: tokenInfo.client_id })}</h2>
          </div>
          <button className="modal-close-btn" onClick={onClose}>
            <X size={16} />
          </button>
        </div>

        <div className="modal-body">
          <div className="alert-box alert-warn mb-4">
            <ShieldAlert size={16} />
            <span>
              <strong>{t('secret_alert_warn')}</strong>
            </span>
          </div>

          <div className="form-group">
            <label>{t('login_token_label')}</label>
            <div className="secret-box">
              <code>{tokenInfo.token}</code>
              <button className="btn btn-secondary btn-copy" onClick={handleCopy}>
                {copied ? <Check size={14} className="text-ok" /> : <Copy size={14} />}
                <span>{copied ? t('copied') : t('copy')}</span>
              </button>
            </div>
          </div>

          <div className="detail-grid mt-4">
            <div className="detail-item">
              <span className="detail-label">{t('th_token_id')}</span>
              <span className="detail-val font-mono">{tokenInfo.id}</span>
            </div>
            <div className="detail-item">
              <span className="detail-label">{t('role_label')}</span>
              <span className="detail-val">{tokenInfo.role}</span>
            </div>
            <div className="detail-item col-span-2">
              <span className="detail-label">{t('token_header_usage')}</span>
              <span className="detail-val font-mono text-xs code-box">
                Authorization: Bearer {tokenInfo.token}
              </span>
            </div>
          </div>
        </div>

        <div className="modal-footer">
          <button className="btn btn-primary" onClick={onClose}>
            {t('btn_done')}
          </button>
        </div>
      </div>
    </div>
  );
};
