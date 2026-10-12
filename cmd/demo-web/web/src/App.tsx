import React, { useState, useEffect, useCallback, useRef } from 'react';
import { useI18n } from './i18n/context';
import { api } from './api';
import { Order, AppConfig, PrinterSummary } from './types';
import { Header } from './components/Header';
import { RollsStrip } from './components/RollsStrip';
import { OrderCard } from './components/OrderCard';
import { Sidebar } from './components/Sidebar';

export const App: React.FC = () => {
  const { t } = useI18n();

  const [config, setConfig] = useState<AppConfig | null>(null);
  const [printers, setPrinters] = useState<PrinterSummary[]>([]);
  const [orders, setOrders] = useState<Order[]>([]);
  const [loading, setLoading] = useState(false);
  const [connected, setConnected] = useState(false);
  const [autoRefresh, setAutoRefresh] = useState(true);

  const [releasingIds, setReleasingIds] = useState<Record<string, boolean>>({});
  const [resendingIds, setResendingIds] = useState<Record<string, boolean>>({});

  const [previewUrl, setPreviewUrl] = useState<string | null>(null);
  const [previewDim, setPreviewDim] = useState<string | null>(null);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);

  const [eventsLog, setEventsLog] = useState<string[]>([]);

  const pushLog = useCallback((msg: string) => {
    const time = new Date().toLocaleTimeString();
    setEventsLog((prev) => [`${time}  ${msg}`, ...prev.slice(0, 35)]);
  }, []);

  const fetchConfigAndFleet = useCallback(async () => {
    try {
      const c = await api.getConfig();
      setConfig(c);
      setPrinters(c.printers || []);
      setConnected(true);
    } catch {
      setConnected(false);
    }
  }, []);

  const fetchOrders = useCallback(async () => {
    try {
      const res = await api.getOrders();
      setOrders(res.items || []);
    } catch {
      // non-fatal
    }
  }, []);

  const refreshAll = useCallback(async () => {
    setLoading(true);
    await Promise.allSettled([fetchConfigAndFleet(), fetchOrders()]);
    setLoading(false);
  }, [fetchConfigAndFleet, fetchOrders]);

  // Initial load
  useEffect(() => {
    refreshAll();
    pushLog(t('events_ready'));
  }, [refreshAll, pushLog, t]);

  // Polling loop
  useEffect(() => {
    if (!autoRefresh) return;
    const interval = setInterval(() => {
      fetchOrders();
    }, 5000);
    return () => clearInterval(interval);
  }, [autoRefresh, fetchOrders]);

  const handleRelease = async (orderId: string) => {
    setReleasingIds((prev) => ({ ...prev, [orderId]: true }));
    try {
      const res = await api.releaseOrder(orderId);
      pushLog(
        t('wave_released_log', {
          order: orderId,
          lines: res.lines,
        })
      );
      await fetchOrders();
    } catch (e: any) {
      pushLog(`Release ${orderId} failed: ${e.message}`);
    } finally {
      setReleasingIds((prev) => ({ ...prev, [orderId]: false }));
    }
  };

  const handleResend = async (orderId: string) => {
    setResendingIds((prev) => ({ ...prev, [orderId]: true }));
    try {
      const res = await api.resendOrder(orderId);
      pushLog(
        t('wave_resend_log', {
          order: orderId,
          lines: res.lines,
          replayed: res.replayed,
        })
      );
      await fetchOrders();
    } catch (e: any) {
      pushLog(`Resend ${orderId} failed: ${e.message}`);
    } finally {
      setResendingIds((prev) => ({ ...prev, [orderId]: false }));
    }
  };

  const handleSelectPrinter = async (orderId: string, lineNo: number, printerId: string) => {
    try {
      await api.setLinePrinter(orderId, lineNo, printerId);
      pushLog(t('rerouted_log', { line: lineNo, printer: printerId }));
      await fetchOrders();
    } catch (e: any) {
      pushLog(`Failed to re-route: ${e.message}`);
    }
  };

  const handlePreview = async (orderId: string, lineNo: number) => {
    const order = orders.find((o) => o.id === orderId);
    if (!order) return;
    const line = order.lines.find((l) => l.no === lineNo);
    if (!line) return;

    setPreviewLoading(true);
    setPreviewError(null);
    setPreviewDim(`${line.paper_size} (${line.width_mm}×${line.height_mm}mm)`);

    const imgUrl = api.getLinePreviewUrl(orderId, lineNo);

    // Pre-test the preview image
    const img = new Image();
    img.onload = () => {
      setPreviewUrl(imgUrl);
      setPreviewLoading(false);
      pushLog(
        t('rendered_preview_log', {
          order: orderId,
          line: lineNo,
          size: line.paper_size,
        })
      );
    };
    img.onerror = () => {
      setPreviewError('Failed to load image preview');
      setPreviewLoading(false);
    };
    img.src = imgUrl;
  };

  return (
    <div className="demo-app">
      <Header
        config={config}
        loading={loading}
        connected={connected}
        autoRefresh={autoRefresh}
        setAutoRefresh={setAutoRefresh}
        onRefresh={refreshAll}
      />

      <RollsStrip printers={printers} />

      <main className="main-layout">
        <section className="orders-section">
          <div className="section-topbar">
            <div>
              <h2>{t('orders_title')}</h2>
              <p className="text-muted text-xs mt-0.5">{t('orders_subtitle')}</p>
            </div>
          </div>

          <div id="orders-list">
            {orders.length > 0 ? (
              orders.map((o) => (
                <OrderCard
                  key={o.id}
                  order={o}
                  printers={printers}
                  releasing={!!releasingIds[o.id]}
                  resending={!!resendingIds[o.id]}
                  onRelease={handleRelease}
                  onResend={handleResend}
                  onSelectPrinter={handleSelectPrinter}
                  onPreview={handlePreview}
                />
              ))
            ) : (
              <div className="card text-center text-muted py-6">{t('no_orders')}</div>
            )}
          </div>
        </section>

        <Sidebar
          previewUrl={previewUrl}
          previewDim={previewDim}
          previewError={previewError}
          previewLoading={previewLoading}
          eventsLog={eventsLog}
        />
      </main>
    </div>
  );
};
