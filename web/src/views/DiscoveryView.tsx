import React, { useState } from 'react';
import { useApp } from '../context/AppContext';
import { useI18n } from '../i18n/context';
import { api } from '../api';
import { Candidate } from '../types';
import { useToast } from '../components/Toast';
import { PlusCircle, EyeOff, RotateCw, CheckCircle2, X } from 'lucide-react';

export const DiscoveryView: React.FC = () => {
  const { candidates, refreshCandidates, refreshPrinters } = useApp();
  const { t } = useI18n();
  const { toast } = useToast();

  const [scanning, setScanning] = useState(false);
  const [adoptingCandidate, setAdoptingCandidate] = useState<Candidate | null>(null);

  const handleScan = async () => {
    setScanning(true);
    try {
      const res = await api.scanDiscovery();
      await refreshCandidates();
      toast(t('scan_complete', { count: res.found }), 'success');
    } catch (e: any) {
      toast(e.message || 'Scan failed', 'error');
    } finally {
      setScanning(false);
    }
  };

  const handleDismiss = async (key: string) => {
    try {
      await api.dismissCandidate(key);
      await refreshCandidates();
      toast(t('candidate_dismissed'), 'info');
    } catch (e: any) {
      toast(e.message || 'Failed to dismiss candidate', 'error');
    }
  };

  return (
    <div className="view-container">
      <div className="view-header">
        <div>
          <h1 className="view-title">{t('discovery_title')}</h1>
          <p className="view-subtitle">{t('discovery_subtitle')}</p>
        </div>

        <div className="header-actions">
          <button className="btn btn-primary" onClick={handleScan} disabled={scanning}>
            <RotateCw size={14} className={scanning ? 'spinning' : ''} />
            <span>{scanning ? t('btn_scanning') : t('btn_scan_network')}</span>
          </button>
        </div>
      </div>

      <div className="table-responsive">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t('th_device_key')}</th>
              <th>{t('th_source')}</th>
              <th>{t('th_model_driver')}</th>
              <th>{t('th_serial')}</th>
              <th>{t('th_discovered_address')}</th>
              <th>{t('th_verified_hi')}</th>
              <th>{t('th_first_seen')}</th>
              <th>{t('th_observed')}</th>
              <th>{t('actions')}</th>
            </tr>
          </thead>
          <tbody>
            {candidates.length > 0 ? (
              candidates.map((c) => {
                const verified = c.detail && c.detail.identified === 'true';
                return (
                  <tr key={c.device_key}>
                    <td className="font-mono text-sm">{c.device_key}</td>
                    <td>
                      <span className="badge-pill">{c.source}</span>
                    </td>
                    <td className="font-mono font-medium">{c.model || '—'}</td>
                    <td className="font-mono text-sm text-muted">{c.serial || '—'}</td>
                    <td className="font-mono text-sm">
                      {c.host ? `${c.host}:${c.port || 9100}` : '—'}
                    </td>
                    <td>
                      {verified ? (
                        <span className="badge-pill badge-ok">
                          <CheckCircle2 size={12} /> {t('verified_yes')}
                        </span>
                      ) : (
                        <span className="badge-pill text-muted">{t('verified_no')}</span>
                      )}
                    </td>
                    <td className="text-muted text-sm">{formatAgo(c.first_seen)}</td>
                    <td className="text-muted text-sm">{formatAgo(c.last_seen)}</td>
                    <td>
                      <div className="table-btn-group">
                        <button
                          className="btn btn-xs btn-primary"
                          onClick={() => setAdoptingCandidate(c)}
                        >
                          <PlusCircle size={12} /> {t('btn_adopt')}
                        </button>
                        <button
                          className="btn btn-xs btn-secondary"
                          onClick={() => handleDismiss(c.device_key)}
                          title={t('btn_dismiss')}
                        >
                          <EyeOff size={12} /> {t('btn_dismiss')}
                        </button>
                      </div>
                    </td>
                  </tr>
                );
              })
            ) : (
              <tr>
                <td colSpan={9} className="text-center text-muted py-6">
                  {t('no_candidates')}
                  <br />
                  <span className="text-xs">{t('no_candidates_sub')}</span>
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {/* Adopt Modal */}
      {adoptingCandidate && (
        <AdoptModal
          candidate={adoptingCandidate}
          onClose={() => setAdoptingCandidate(null)}
          onSuccess={async () => {
            setAdoptingCandidate(null);
            await refreshCandidates();
            await refreshPrinters();
          }}
        />
      )}
    </div>
  );
};

interface AdoptModalProps {
  candidate: Candidate;
  onClose: () => void;
  onSuccess: () => void;
}

const AdoptModal: React.FC<AdoptModalProps> = ({ candidate: c, onClose, onSuccess }) => {
  const { t } = useI18n();
  const { toast } = useToast();

  const [printerId, setPrinterId] = useState(
    c.serial ? `wh1-${c.serial.toLowerCase()}` : `wh1-printer-01`
  );
  const [site, setSite] = useState(c.site || 'wh1');
  const [name, setName] = useState(c.model ? `${c.model} (Packing)` : 'Packing Station Printer');
  const [customLabel, setCustomLabel] = useState('');
  const [remark, setRemark] = useState('');
  const [loading, setLoading] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!printerId.trim()) {
      toast('Printer ID is required', 'warning');
      return;
    }

    setLoading(true);
    try {
      await api.adoptCandidate(c.device_key, {
        printer_id: printerId.trim(),
        site: site.trim(),
        name: name.trim(),
        custom_label: customLabel.trim(),
        remark: remark.trim(),
      });
      toast(t('adopt_success', { id: printerId }), 'success');
      onSuccess();
    } catch (e: any) {
      toast(e.message || 'Failed to adopt candidate', 'error');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal-card" onClick={(e) => e.stopPropagation()}>
        <div className="modal-header">
          <div className="modal-title-wrap">
            <PlusCircle size={18} className="text-accent" />
            <h2>{t('adopt_title')}</h2>
          </div>
          <button className="modal-close-btn" onClick={onClose}>
            <X size={16} />
          </button>
        </div>

        <form onSubmit={handleSubmit}>
          <div className="modal-body">
            <p className="text-muted text-sm mb-4">
              {t('adopt_desc', { device: c.device_key, addr: `${c.host}:${c.port || 9100}` })}
            </p>

            <div className="form-group">
              <label>{t('adopt_printer_id_label')}</label>
              <input
                type="text"
                value={printerId}
                onChange={(e) => setPrinterId(e.target.value)}
                placeholder="e.g. wh1-pack-04"
                required
                autoFocus
              />
            </div>

            <div className="form-group">
              <label>{t('adopt_site_label')}</label>
              <input
                type="text"
                value={site}
                onChange={(e) => setSite(e.target.value)}
                placeholder="e.g. wh1"
              />
            </div>

            <div className="form-group">
              <label>{t('adopt_name_label')}</label>
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g. Packing Station 4 (ZT411)"
              />
            </div>

            <div className="form-group">
              <label>{t('custom_label')}</label>
              <input
                type="text"
                value={customLabel}
                onChange={(e) => setCustomLabel(e.target.value)}
                placeholder={t('custom_label_placeholder')}
              />
            </div>

            <div className="form-group">
              <label>{t('remark')}</label>
              <input
                type="text"
                value={remark}
                onChange={(e) => setRemark(e.target.value)}
                placeholder={t('remark_placeholder')}
              />
            </div>
          </div>

          <div className="modal-footer">
            <button type="button" className="btn btn-secondary" onClick={onClose}>
              {t('cancel')}
            </button>
            <button type="submit" className="btn btn-primary" disabled={loading}>
              {loading ? t('btn_adopting') : t('btn_confirm_adoption')}
            </button>
          </div>
        </form>
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
