import { AppConfig, Order } from './types';

async function request<T = any>(path: string, options: RequestInit = {}): Promise<T> {
  const res = await fetch(path, options);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new Error(data.error || res.statusText || `HTTP ${res.status}`);
  }
  return data;
}

export const api = {
  getConfig: () => request<AppConfig>('/api/config'),
  getOrders: () => request<{ items: Order[]; count: number }>('/api/orders'),
  releaseOrder: (id: string) =>
    request<{ order: string; action: string; lines: number; replayed: number }>(
      `/api/orders/${encodeURIComponent(id)}/release`,
      { method: 'POST' }
    ),
  resendOrder: (id: string) =>
    request<{ order: string; action: string; lines: number; replayed: number }>(
      `/api/orders/${encodeURIComponent(id)}/resend`,
      { method: 'POST' }
    ),
  setLinePrinter: (id: string, lineNo: number, printerId: string) =>
    request<{ ok: boolean; printer_id: string }>(
      `/api/orders/${encodeURIComponent(id)}/lines/${lineNo}/printer`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ printer_id: printerId }),
      }
    ),
  getLinePreviewUrl: (id: string, lineNo: number) =>
    `/api/orders/${encodeURIComponent(id)}/lines/${lineNo}/preview?t=${Date.now()}`,
};
