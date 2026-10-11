/* WMS packing console.
   Deliberately thin: it calls this binary's own API, which is where the reference
   integration logic lives (cmd/demo-web/client.go). */
'use strict';
const $ = (s) => document.querySelector(s);
const api = async (path, opts = {}) => {
  const r = await fetch(path, opts);
  const d = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(d.error || r.statusText);
  return d;
};

async function loadConfig() {
  const c = await api('/api/config');
  $('#cfg').textContent = `${c.cps} · ${c.printers.length} printer(s)${c.preview ? ' · preview on' : ''}`;
  $('#cfg').className = 'pill ' + (c.printers.length ? 'ok' : 'bad');
  $('#preview-card').hidden = !c.preview;
  return c;
}

async function loadOrders() {
  const { items } = await api('/api/orders');
  $('#orders').innerHTML = items.map(o => `
    <div class="order" data-id="${o.id}">
      <div class="top">
        <span class="id">${o.id}</span>
        <span class="meta">${o.customer} · ${o.sku} x${o.qty} · bin ${o.bin}</span>
        <span class="spacer"></span>
        <button class="btn primary" data-act="release">Release wave</button>
        <button class="btn" data-act="resend" title="Re-submit every line unchanged — must not print a second label">Resend (idempotency test)</button>
      </div>
      <div class="lines">${o.lines.map(l => lineHTML(o, l)).join('')}</div>
    </div>`).join('') || '<div class="order muted">No orders.</div>';
  bindOrderEvents();
}

function lineHTML(o, l) {
  const s = l.status || {};
  const tags = [];
  if (s.replayed) tags.push('replayed');
  if (s.attempts > 1) tags.push(`${s.attempts} attempts`);
  if (s.error) tags.push(s.error);
  const bytes = s.job ? `${s.job.payload_bytes} B` : '';
  const previewBtn = (window.__preview ? `<button class="btn" data-act="preview">preview</button>` : '');
  return `<div class="line">
    <span class="no">L${l.no}</span>
    <span class="state s-${s.state || 'queued'}">${s.state || 'not released'}</span>
    <span class="tag">${o.id}-line-${l.no}</span>
    <span class="tag">${tags.join(' · ')}</span>
    <span class="tag">${bytes}</span>
    <span class="spacer"></span>${previewBtn}
  </div>`;
}

function bindOrderEvents() {
  document.querySelectorAll('.order').forEach(card => {
    const id = card.dataset.id;
    card.querySelectorAll('[data-act]').forEach(btn => {
      btn.onclick = async () => {
        const act = btn.dataset.act;
        btn.disabled = true;
        try {
          if (act === 'preview') return await preview(card.dataset.id, btn);
          const r = await api(`/api/orders/${id}/${act}`, { method: 'POST' });
          push(`#events`, `${act} ${id}: ${r.lines} line(s), ${r.replayed} replayed`);
          await loadOrders();
        } catch (e) {
          push(`#events`, `${act} ${id}: ${e.message}`);
          btn.disabled = false;
        }
      };
    });
  });
}

async function preview(orderId, btn) {
  const { items } = await api('/api/orders');
  const order = items.find(o => o.id === orderId);
  const zpl = order.lines[0].zpl || '';
  push('#events', `preview ${orderId}…`);
  const r = await fetch('/api/preview', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ zpl }),
  });
  const slot = $('#preview-slot');
  if (!r.ok) { slot.textContent = (await r.json()).error; return; }
  slot.innerHTML = `<img class="preview" alt="rendered label">`;
  slot.querySelector('img').src = URL.createObjectURL(await r.blob());
}

function push(sel, line) {
  const el = $(sel);
  const t = new Date().toLocaleTimeString();
  el.textContent = `${t}  ${line}\n` + el.textContent.split('\n').slice(0, 40).join('\n');
}

async function stream() {
  // The console's own API does not proxy SSE (it is not needed): poll job state instead.
  const live = $('#live');
  const timer = setInterval(async () => {
    try { await api('/api/config'); live.textContent = 'online'; live.className = 'pill ok'; }
    catch { live.textContent = 'offline'; live.className = 'pill bad'; }
  }, 5000);
  return () => clearInterval(timer);
}

(async () => {
  try {
    const c = await loadConfig();
    window.__preview = c.preview;
    await loadOrders();
    $('#reload').onclick = loadOrders;
    $('#autorefresh').onchange = (e) => {
      if (e.target.checked) setInterval(loadOrders, 4000);
    };
    await stream();
    push('#events', 'console ready');
  } catch (e) {
    $('#cfg').textContent = 'error: ' + e.message;
    $('#cfg').className = 'pill bad';
  }
})();
