export interface PrinterSummary {
  printer_id: string;
  name: string;
  site: string;
  model?: string;
  paper_size: string;
  media_width_mm: number;
  media_height_mm: number;
  remark?: string;
  custom_label?: string;
  usable: boolean;
  enabled: boolean;
  queue_depth: number;
}

export interface DispatchResult {
  job_id?: string;
  state: 'queued' | 'sending' | 'printed' | 'sent_unknown' | 'failed' | 'rejected' | 'error';
  replayed: boolean;
  attempts: number;
  error?: string;
  latency_ms: number;
  updated_at: string;
}

export interface Line {
  no: number;
  label_type: string;
  label_name: string;
  paper_size: string;
  width_mm: number;
  height_mm: number;
  selected_printer?: string;
  target_printer: string;
  status?: DispatchResult;
  job_id?: string;
}

export interface Order {
  id: string;
  customer: string;
  sku: string;
  qty: number;
  bin: string;
  lines: Line[];
  created: string;
}

export interface AppConfig {
  cps: string;
  site: string;
  printer: string;
  printers: PrinterSummary[];
  preview: boolean;
  simulate: number;
  token_kind: string;
}
