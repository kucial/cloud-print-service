import {
  FleetStatusResponse,
  BulkJobStatusResponse,
  Printer,
  Job,
  Agent,
  Candidate,
  TokenItem,
  IssuedTokenResponse,
  JobAttempt,
  RenderPreviewMeta,
} from './types';

const API_BASE = '/api/v1';
export const TOKEN_KEY = 'cps.token';
export const THEME_KEY = 'cps.theme';

export function getStoredToken(): string {
  return localStorage.getItem(TOKEN_KEY) || '';
}

export function setStoredToken(token: string) {
  if (token) {
    localStorage.setItem(TOKEN_KEY, token);
  } else {
    localStorage.removeItem(TOKEN_KEY);
  }
}

export async function request<T = any>(
  path: string,
  options: {
    method?: string;
    body?: any;
    token?: string;
    raw?: boolean;
  } = {}
): Promise<T> {
  const token = options.token !== undefined ? options.token : getStoredToken();
  const headers: Record<string, string> = {};

  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }
  if (options.body && !(options.body instanceof FormData)) {
    headers['Content-Type'] = 'application/json';
  }

  const url = path.startsWith('/api') ? path : `${API_BASE}${path}`;
  const res = await fetch(url, {
    method: options.method || 'GET',
    headers,
    body: options.body
      ? options.body instanceof FormData
        ? options.body
        : JSON.stringify(options.body)
      : undefined,
  });

  if (options.raw) {
    return res as unknown as T;
  }

  const text = await res.text();
  let data: any = {};
  try {
    data = text ? JSON.parse(text) : {};
  } catch {
    data = { raw: text };
  }

  if (!res.ok) {
    const code = data?.error?.code || res.status;
    const msg = data?.error?.message || text || res.statusText;
    const err = new Error(`${code}: ${msg}`);
    (err as any).status = res.status;
    (err as any).code = code;
    throw err;
  }

  return data;
}

// Typed API Helpers
export const api = {
  getFleet: () => request<FleetStatusResponse>('/printers/status'),
  getPrinter: (id: string) => request<Printer>(`/printers/${id}`),
  updatePrinter: (
    id: string,
    body: {
      name?: string;
      site?: string;
      remark?: string;
      custom_label?: string;
      paper_size?: string;
      media_width_mm?: number;
      media_height_mm?: number;
      dpi?: number;
      dpmm?: number;
      orientation?: string;
      media_orientation?: string;
      print_direction?: string;
      cjk_supported?: boolean;
      cjk_font?: string;
      fonts?: string[];
      enabled?: boolean;
    }
  ) => request<Printer>(`/printers/${id}`, { method: 'PATCH', body }),
  testPrint: (id: string) => request(`/printers/${id}/test`, { method: 'POST' }),
  diagnostics: (id: string) => request<Diagnostics>(`/printers/${id}/diagnostics`),
  maintenance: (id: string, action: 'calibrate' | 'label_top', dots?: number) =>
    request<{ sent: string; note: string }>(`/printers/${id}/maintenance`, {
      method: 'POST',
      body: { action, dots },
    }),
  probePrinters: (id?: string) =>
    request(id ? `/printers/${id}/status` : '/printers/probe', {
      method: id ? 'GET' : 'POST',
    }),

  getJobs: (limit = 50, printerId?: string, state?: string, ids?: string[]) => {
    const q = new URLSearchParams();
    if (limit) q.set('limit', String(limit));
    if (printerId) q.set('printer_id', printerId);
    if (state) q.set('state', state);
    if (ids && ids.length > 0) q.set('ids', ids.join(','));
    return request<{ items: Job[]; count: number; summary?: Record<string, number> }>(`/jobs?${q.toString()}`);
  },
  getBulkJobStatus: (ids: string[]) =>
    request<BulkJobStatusResponse>(`/jobs/status?ids=${encodeURIComponent(ids.join(','))}`),
  getJobAttempts: (jobId: string) =>
    request<{ items: JobAttempt[]; count: number }>(`/jobs/${jobId}/attempts`),
  cancelJob: (jobId: string) => request<Job>(`/jobs/${jobId}/cancel`, { method: 'POST' }),
  resolveJob: (jobId: string, resolution: 'printed' | 'failed', note?: string) =>
    request<Job>(`/jobs/${jobId}/resolve`, { method: 'POST', body: { resolution, note } }),

  getAgents: () => request<{ items: Agent[]; count: number; healthy: number }>('/agents'),
  removeAgent: (agentId: string) =>
    request(`/agents/${agentId}`, { method: 'DELETE' }),

  getCandidates: (includeDismissed = false) =>
    request<{ items: Candidate[]; count: number }>(
      `/admin/discovery/candidates?dismissed=${includeDismissed}`
    ),
  scanDiscovery: () => request('/admin/discovery/scan', { method: 'POST' }),
  adoptCandidate: (
    key: string,
    body: {
      printer_id: string;
      site?: string;
      name?: string;
      remark?: string;
      custom_label?: string;
      paper_size?: string;
      media_width_mm?: number;
      media_height_mm?: number;
      dpi?: number;
      dpmm?: number;
      orientation?: string;
      media_orientation?: string;
      print_direction?: string;
      cjk_supported?: boolean;
      cjk_font?: string;
      fonts?: string[];
    }
  ) => request(`/admin/discovery/candidates/${key}/adopt`, { method: 'POST', body }),
  dismissCandidate: (key: string) =>
    request(`/admin/discovery/candidates/${key}/dismiss`, { method: 'POST' }),

  getTokens: (clientId?: string) => {
    const q = clientId ? `?client_id=${encodeURIComponent(clientId)}` : '';
    return request<{ items: TokenItem[]; count: number; active: number }>(
      `/admin/tokens${q}`
    );
  },
  issueToken: (body: { client_id: string; role: string; ttl?: string; note?: string }) =>
    request<IssuedTokenResponse>('/admin/tokens', { method: 'POST', body }),
  rotateToken: (id: string) =>
    request<IssuedTokenResponse>(`/admin/tokens/${id}/rotate`, { method: 'POST' }),
  revokeToken: (id: string) => request(`/admin/tokens/${id}`, { method: 'DELETE' }),

  renderPreview: async (body: {
    zpl: string;
    width_mm?: number;
    height_mm?: number;
    dpmm?: number;
    grayscale?: boolean;
    upright?: boolean;
    format?: string;
  }): Promise<{ blob: Blob; meta: RenderPreviewMeta }> => {
    const res = await request<Response>('/admin/preview', {
      method: 'POST',
      body,
      raw: true,
    });
    if (!res.ok) {
      const errJson = await res.json().catch(() => ({}));
      throw new Error(errJson?.error?.message || `Render failed (${res.status})`);
    }
    const blob = await res.blob();
    const h = res.headers;
    const meta: RenderPreviewMeta = {
      backend: h.get('X-Render-Backend') || 'unknown',
      format: h.get('X-Render-Format') || 'png',
      width_dots: Number(h.get('X-Render-Width-Dots')) || 0,
      height_dots: Number(h.get('X-Render-Height-Dots')) || 0,
      duration_ms: Number(h.get('X-Render-Duration-Ms')) || 0,
      warnings: h.get('X-Render-Warnings') || undefined,
    };
    return { blob, meta };
  },

  getMetrics: () =>
    fetch('/metrics').then((r) => (r.ok ? r.text() : Promise.reject('Failed to fetch metrics'))),
  getHealthz: () => fetch('/healthz').then((r) => r.json()),
  getReadyz: () => fetch('/readyz').then((r) => r.json()),
};

export interface Diagnostics {
  printer_id: string;
  checked_at: string;
  identity?: { model: string; firmware?: string; serial?: string; dpmm?: number };
  status?: { state: string; detail?: string; raw?: string };
  config?: {
    settings: Record<string, string>;
    dpi?: number;
    dpmm?: number;
    print_width_mm?: number;
    print_width_dots?: number;
    label_length_mm?: number;
    label_length_dots?: number;
    label_top_dots: number;
    left_position_dots: number;
    darkness?: string;
    print_speed?: string;
    print_method?: string;
    print_mode?: string;
    media_type?: string;
    language?: string;
    firmware?: string;
    head_usage?: string;
  };
  declared: { paper_size?: string; media_width_mm?: number; media_height_mm?: number; dpi?: number };
  warnings: { code: string; message: string }[];
  errors?: Record<string, string>;
  raw_config?: string;
}
