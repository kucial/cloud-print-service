export const en = {
  // Brand & Topbar
  brand_title: 'WMS Packing Console',
  brand_sub: 'Multi-Roll Size Auto-Routing Reference',
  connected: 'Connected',
  connecting: 'Connecting…',
  refresh: 'Refresh',
  auto_refresh: 'Auto',
  lang_switch: 'Language / 语言',

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
