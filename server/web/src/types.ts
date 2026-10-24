export type PrinterState =
  | 'idle'
  | 'printing'
  | 'busy'
  | 'media_low'
  | 'media_out'
  | 'ribbon_low'
  | 'ribbon_out'
  | 'head_error'
  | 'error'
  | 'offline'
  | 'disabled'
  | 'door_open'
  | 'paper_jam'
  | 'toner_low'
  | 'toner_out'
  | 'unknown';

export type PrinterKind = 'label' | 'receipt' | 'document';

export interface JobRef {
  job_id: string;
  state: string;
  at: string;
}

export interface PrinterStatus {
  state: PrinterState;
  raw?: string;
  detail?: string;
  observed_at: string;
  stale: boolean;
  last_job?: JobRef;
}

export interface Printer {
  printer_id: string;
  name: string;
  site: string;
  model: string;
  driver: string;
  // What the printer is for, what it takes as is, and which tools it has beyond printing
  // (diagnostics, maintenance and fonts are label-printer tools).
  kind?: PrinterKind;
  formats?: string[];
  capabilities?: string[];
  host: string;
  port: number;
  enabled: boolean;
  usable: boolean;
  queue_depth: number;
  remark?: string;
  custom_label?: string;
  paper_size?: string;
  media_width_mm?: number;
  media_height_mm?: number;
  dpi?: number;
  dpmm?: number;
  resolution_source?: 'hi' | 'config' | 'default';
  dpi_resolved_from_hi?: boolean;
  orientation?: string;
  media_orientation?: string;
  print_direction?: string;
  cjk_supported?: boolean;
  cjk_font?: string;
  available_fonts?: { name: string; size: number; cjk?: boolean }[] | null;
  fonts?: string[];
  font_source?: 'hi' | 'config' | 'default';
  font_resolved_from_hi?: boolean;
  status: PrinterStatus;
}

export interface FleetStatusResponse {
  count: number;
  usable: number;
  not_usable: number;
  observed_at: string;
  items: Printer[];
}

export interface BulkJobStatusResponse {
  items: Job[];
  count: number;
  summary: {
    queued: number;
    sending: number;
    printed: number;
    sent_unknown: number;
    failed: number;
    cancelled: number;
  };
}

export interface Job {
  job_id: string;
  client_id: string;
  external_id: string;
  printer_id: string;
  state: 'queued' | 'sending' | 'printed' | 'sent_unknown' | 'failed' | 'cancelled';
  state_detail?: string;
  status_message?: string;
  payload_bytes: number;
  payload_sha256?: string;
  copies: number;
  priority: string;
  biz_ref: string;
  attempts: number;
  last_error?: string;
  partial_write: boolean;
  queued_at: string;
  sent_at?: string;
  finished_at?: string;
}

export interface JobAttempt {
  id: number;
  job_id: string;
  attempt_no: number;
  started_at: string;
  finished_at?: string;
  result: string;
  bytes_sent: number;
  error?: string;
}

export interface Agent {
  id: string;
  site: string;
  hostname: string;
  os: string;
  version: string;
  agent_addr: string;
  last_seen: string;
  printers: string[];
}

export interface Candidate {
  device_key: string;
  source: string;
  vendor?: string;
  model?: string;
  serial?: string;
  host?: string;
  port?: number;
  site?: string;
  detail?: Record<string, string>;
  first_seen: string;
  last_seen: string;
  dismissed: boolean;
}

export interface TokenItem {
  id: string;
  client_id: string;
  role: 'client' | 'admin';
  kind: string;
  prefix: string;
  note?: string;
  created_at: string;
  expires_at?: string;
  revoked_at?: string;
  last_used_at?: string;
  uses: number;
}

export interface AgentEnrollResponse {
  token: string;
  agent_id: string;
  prefix: string;
  expires?: string;
  enroll: string;
  warning?: string;
}

export interface IssuedTokenResponse {
  id: string;
  client_id: string;
  role: string;
  note?: string;
  token: string;
  prefix: string;
  created_at: string;
  expires_at?: string;
  usage: string;
  warning?: string;
}

export interface RenderPreviewMeta {
  backend: string;
  format: string;
  width_dots: number;
  height_dots: number;
  duration_ms: number;
  warnings?: string;
}
