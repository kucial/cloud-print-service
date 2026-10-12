export const en = {
  // Brand & Topbar
  brand_title: 'WMS Packing Console',
  brand_sub: 'Multi-Roll Size Auto-Routing Reference',
  nav_orders: 'Packaging Station',
  nav_inspector: 'Raw API Inspector',
  connected: 'Connected',
  connecting: 'Connecting…',
  refresh: 'Refresh',
  auto_refresh: 'Auto',
  lang_switch: 'Language / 语言',
  theme_light: 'Switch to light mode',
  theme_dark: 'Switch to dark mode',

  // API Inspector
  inspector_title: 'Raw HTTP API Inspector',
  inspector_subtitle: 'Live wire capture of all HTTP requests and responses between business-demo and Cloud Print Service.',
  filter_all: 'All Traces',
  filter_dispatch: 'Job Dispatch',
  filter_fleet: 'Fleet Status',
  filter_preview: 'ZPL Preview',
  clear_history: 'Clear History',
  no_traces: 'No captured API traffic yet. Release a wave or trigger a sample request above.',
  select_trace_prompt: 'Select an API request from the list to inspect its raw HTTP headers and payload.',
  quick_triggers: 'Interactive API Triggers:',
  trigger_fleet: 'GET /printers/status',
  trigger_job: 'POST /jobs (4x6)',
  trigger_preview: 'POST /admin/preview',
  copy_curl: 'Copy cURL',
  copy_req: 'Copy Request',
  copy_res: 'Copy Response',
  copied_curl: 'cURL command copied!',
  copied_req: 'Request payload copied!',
  copied_res: 'Response payload copied!',
  section_request: 'HTTP Request',
  section_response: 'HTTP Response',
  request_headers: 'Request Headers',
  response_headers: 'Response Headers',
  request_body: 'Request Payload',
  response_body: 'Response Payload',

  // Rolls Strip
  strip_label: 'Installed Printer Rolls',
  strip_sub: 'Printers are dedicated to specific roll sizes in this warehouse',
  no_printers_registered: 'No printers registered on server.',
  usable: 'Usable',
  offline: 'Offline',

  // Orders Section
  orders_title: 'Outbound Orders (Multi-Spec Package Waves)',
  orders_subtitle: 'Each line produces a distinct document format and auto-routes to a printer loaded with that exact roll size.',
  no_orders: 'No orders available.',
  release_wave: 'Release Wave',
  resend_wave: 'Resend (Idempotency)',
  releasing: 'Releasing…',
  resending: 'Resending…',
  replayed: 'replayed',
  attempts: '{count} attempts',

  // Lines
  roll_printer_label: 'Roll Printer:',
  no_compatible_printer: 'No printers available',
  size_mismatch: 'Size Mismatch',
  size_mismatch_title: 'Installed roll size does not match document requirements!',
  preview_btn: 'Preview',
  not_released: 'Not Released',

  // Statuses
  state_queued: 'Queued',
  state_sending: 'Sending',
  state_printed: 'Printed',
  state_sent_unknown: 'Sent Unknown',
  state_failed: 'Failed',
  state_rejected: 'Rejected',
  state_error: 'Error',

  // Sidebar: Architecture
  arch_title: 'Architecture: Paper Size Routing',
  arch_desc: 'Label printers cannot change paper on the fly. In warehouse automation, each packaging station utilizes distinct roll sizes for specific workflows:',
  roll_4x6_title: 'Pallet / Outbound Shipping',
  roll_4x6_desc: '101.6 × 152.4 mm · Full Carrier logistics format',
  roll_4x2_title: 'Carton Box / Container',
  roll_4x2_desc: '101.6 × 50.8 mm · Packing station carton barcode',
  roll_2x1_title: 'Item SKU / Bin Pick Tag',
  roll_2x1_desc: '50.8 × 25.4 mm · Individual piece tag & barcode',
  arch_idempotency_rule: 'Idempotency Rule: Re-dispatching an order replays the exact same external_id per line, guaranteeing zero double-printing.',

  // Sidebar: Preview
  preview_title: 'Rendered Label Preview',
  preview_rendering: 'Rendering template…',
  preview_empty_prompt: 'Click "Preview" on any line item to render its exact aspect ratio.',
  preview_error: 'Render error: {error}',

  // Sidebar: Events
  events_title: 'Live Dispatch Stream',
  events_ready: 'WMS packing console online',
  wave_released_log: 'Wave released {order}: {lines} line(s) routed to roll printers',
  wave_resend_log: 'Wave resend {order}: {lines} line(s) replayed ({replayed} deduplicated)',
  rerouted_log: 'Line L{line} re-routed to {printer}',
  rendered_preview_log: 'Rendered preview for {order} Line L{line} ({size})',
};

export type TranslationKey = keyof typeof en;
