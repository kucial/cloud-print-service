/* WMS Packing Console — Multi-Roll Size Auto-Routing Demo
 * Demonstrates:
 *   1. Roll size matching: 4x6 -> 4x6 printer, 4x2 -> 4x2 printer, 2x1 -> 2x1 printer
 *   2. Operator manual override per line
 *   3. Idempotent wave dispatch (zero duplicate labels on resend)
 *   4. Preview rendering with exact label dimensions
 */
'use strict';

const $ = (s) => document.querySelector(s);
let cachedPrinters = [];

async function api(path, opts = {}) {
  const r = await fetch(path, opts);
  const d = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(d.error || r.statusText);
  return d;
}

function pushLog(msg) {
  const box = $('#events-log');
  if (!box) return;
  const time = new Date().toLocaleTimeString();
  box.textContent = `${time}  ${msg}\n` + box.textContent.split('\n').slice(0, 30).join('\n');
}

async function loadConfig() {
  const c = await api('/api/config');
  $('#server-url').textContent = `${c.cps}`;
  $('#preview-card').hidden = !c.preview;
  cachedPrinters = c.printers || [];
  renderRollsFleet(cachedPrinters);
}

function renderRollsFleet(printers) {
  const box = $('#rolls-fleet');
  if (!box) return;

  if (!printers.length) {
    box.innerHTML = `<div class="text-muted text-sm py-2">No printers registered on server.</div>`;
    return;
  }

  box.innerHTML = printers.map((p) => {
    const rollClass = p.paper_size === '4x6' ? 'roll-4x6' : p.paper_size === '4x2' ? 'roll-4x2' : 'roll-2x1';
    return `
      <div class="roll-printer-card ${!p.usable ? 'unusable' : ''}">
        <div class="roll-info">
          <div class="flex items-center gap-2">
            <span class="roll-printer-id">${p.printer_id}</span>
            ${p.custom_label ? `<span class="badge-pill font-mono">${p.custom_label}</span>` : ''}
          </div>
          <div class="roll-printer-meta">
            ${p.site || 'no-site'} · ${p.model || 'Zebra'} ${p.media_width_mm && p.media_height_mm ? `(${p.media_width_mm}×${p.media_height_mm}mm)` : ''}
          </div>
        </div>
        <div class="flex flex-col items-end gap-1">
          <span class="badge-roll ${rollClass}">${p.paper_size || 'custom'}</span>
          <span class="text-xs ${p.usable ? 'text-ok' : 'text-bad'}">${p.usable ? 'Usable' : 'Offline'}</span>
        </div>
      </div>
    `;
  }).join('');
}

async function loadOrders() {
  const { items } = await api('/api/orders');
  const box = $('#orders-list');
  if (!box) return;

  if (!items || !items.length) {
    box.innerHTML = `<div class="order-card text-muted text-center py-6">No orders available.</div>`;
    return;
  }

  box.innerHTML = items.map((o) => `
    <div class="order-card" data-order="${o.id}">
      <div class="order-top">
        <div>
          <span class="order-id-badge">${o.id}</span>
          <span class="order-customer">${o.customer}</span>
          <span class="text-muted text-xs font-mono ml-2">SKU: ${o.sku} x${o.qty} · Bin ${o.bin}</span>
        </div>
        <div class="order-actions">
          <button class="btn btn-sm btn-primary" data-action="release">
            Release Wave
          </button>
          <button class="btn btn-sm btn-secondary" data-action="resend" title="Re-submit identical wave to prove zero-duplicate printing">
            Resend (Idempotency)
          </button>
        </div>
      </div>

      <div class="lines-container">
        ${o.lines.map((l) => renderLineRow(o, l)).join('')}
      </div>
    </div>
  `).join('');

  bindActions();
}

function renderLineRow(order, line) {
  const s = line.status || {};
  const rollClass = line.paper_size === '4x6' ? 'roll-4x6' : line.paper_size === '4x2' ? 'roll-4x2' : 'roll-2x1';
  const icon = line.paper_size === '4x6' ? '📦' : line.paper_size === '4x2' ? '🗃️' : '🏷️';

  // Printer select options
  const printerOptions = cachedPrinters.map((p) => {
    const isMatched = p.paper_size === line.paper_size;
    const isSelected = p.printer_id === (line.selected_printer || line.target_printer);
    return `<option value="${p.printer_id}" ${isSelected ? 'selected' : ''}>
      ${p.printer_id} [${p.paper_size || 'any'}] ${!p.usable ? '(offline)' : ''} ${isMatched ? '★' : ''}
    </option>`;
  }).join('');

  const assigned = cachedPrinters.find((p) => p.printer_id === line.target_printer);
  const isMismatch = assigned && assigned.paper_size && assigned.paper_size !== line.paper_size;

  return `
    <div class="line-item-row" data-line="${line.no}">
      <div class="line-spec-badge">
        <span class="line-icon">${icon}</span>
        <div>
          <div class="line-spec-title">${line.label_name}</div>
          <div class="line-spec-size">${line.width_mm} × ${line.height_mm} mm</div>
        </div>
        <span class="badge-roll ${rollClass}">${line.paper_size}</span>
      </div>

      <div class="line-routing">
        <span class="text-xs text-muted">Roll Printer:</span>
        <select class="printer-select" data-line-select="${line.no}">
          ${printerOptions || '<option value="">No printers</option>'}
        </select>
        ${isMismatch ? `<span class="badge-pill badge-danger text-xs" title="Installed roll size does not match document!">Size Mismatch</span>` : ''}
      </div>

      <div class="line-status-wrap">
        <button class="btn btn-xs btn-secondary" data-preview-line="${line.no}">
          Preview
        </button>
        <span class="state-badge s-${s.state || 'queued'}">
          ${s.state || 'Not Released'}
        </span>
        ${s.replayed ? `<span class="badge-pill badge-ok">replayed</span>` : ''}
        ${s.attempts > 1 ? `<span class="badge-pill">${s.attempts} tries</span>` : ''}
      </div>
    </div>
  `;
}

function bindActions() {
  document.querySelectorAll('.order-card').forEach((card) => {
    const orderId = card.dataset.order;

    // Release button
    const releaseBtn = card.querySelector('[data-action="release"]');
    if (releaseBtn) {
      releaseBtn.onclick = async () => {
        releaseBtn.disabled = true;
        try {
          const res = await api(`/api/orders/${orderId}/release`, { method: 'POST' });
          pushLog(`Wave released ${orderId}: ${res.lines} line(s) routed to roll printers`);
          await loadOrders();
        } catch (e) {
          pushLog(`Release failed ${orderId}: ${e.message}`);
        } finally {
          releaseBtn.disabled = false;
        }
      };
    }

    // Resend button (Idempotency test)
    const resendBtn = card.querySelector('[data-action="resend"]');
    if (resendBtn) {
      resendBtn.onclick = async () => {
        resendBtn.disabled = true;
        try {
          const res = await api(`/api/orders/${orderId}/resend`, { method: 'POST' });
          pushLog(`Wave resend ${orderId}: ${res.lines} line(s) replayed (${res.replayed} deduplicated)`);
          await loadOrders();
        } catch (e) {
          pushLog(`Resend failed ${orderId}: ${e.message}`);
        } finally {
          resendBtn.disabled = false;
        }
      };
    }

    // Printer select change per line
    card.querySelectorAll('[data-line-select]').forEach((sel) => {
      sel.onchange = async () => {
        const lineNo = sel.dataset.lineSelect;
        const printerId = sel.value;
        try {
          await api(`/api/orders/${orderId}/lines/${lineNo}/printer`, {
            method: 'POST',
            body: JSON.stringify({ printer_id: printerId }),
          });
          pushLog(`Line L${lineNo} re-routed to ${printerId}`);
          await loadOrders();
        } catch (e) {
          pushLog(`Failed to re-route L${lineNo}: ${e.message}`);
        }
      };
    });

    // Preview button per line
    card.querySelectorAll('[data-preview-line]').forEach((btn) => {
      btn.onclick = async () => {
        const lineNo = parseInt(btn.dataset.previewLine, 10);
        await renderLinePreview(orderId, lineNo);
      };
    });
  });
}

async function renderLinePreview(orderId, lineNo) {
  const { items } = await api('/api/orders');
  const order = items.find((o) => o.id === orderId);
  if (!order) return;
  const line = order.lines.find((l) => l.no === lineNo);
  if (!line) return;

  const slot = $('#preview-slot');
  const dim = $('#preview-dim');
  if (!slot) return;

  slot.textContent = 'Rendering template…';
  if (dim) dim.textContent = `${line.paper_size} (${line.width_mm}×${line.height_mm}mm)`;

  const imgUrl = `/api/orders/${encodeURIComponent(orderId)}/lines/${lineNo}/preview?t=${Date.now()}`;
  slot.innerHTML = `<img src="${imgUrl}" alt="Label Preview for ${line.label_name}" />`;
  pushLog(`Rendered preview for ${orderId} Line L${lineNo} (${line.paper_size})`);
}

// Background sync
(async () => {
  try {
    await loadConfig();
    await loadOrders();
    pushLog('WMS packing console online');

    // Refresh every 5s
    setInterval(async () => {
      await loadOrders();
    }, 5000);

    const reloadBtn = $('#reload');
    if (reloadBtn) {
      reloadBtn.onclick = async () => {
        await loadConfig();
        await loadOrders();
        pushLog('Fleet and orders refreshed');
      };
    }
  } catch (e) {
    $('#server-url').textContent = 'error: ' + e.message;
  }
})();
