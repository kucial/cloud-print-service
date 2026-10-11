/* Cloud Print Service operator panel.
 *
 * Dependency-free by design: the panel ships inside the binary and must work on a
 * warehouse laptop with no internet, so no framework, no build step, no CDN.
 *
 * Auth model matches the API: bearer token in localStorage, sent on every request.
 * Any admin token works — a bootstrap key from config, or one issued via
 * POST /api/v1/admin/tokens.
 */
'use strict';

const API = '/api/v1';
const STORE = { token: 'cps.token', client: 'cps.client', theme: 'cps.theme' };

const state = {
  token: localStorage.getItem(STORE.token) || '',
  client: '',
  role: '',
  route: 'overview',
  printers: [],
  jobs: [],
  tokens: [],
  selected: null,
  filter: { state: '', printer: '' },
  live: false,
  abort: null,
  health: null,
};

/* ------------------------------------------------------------------ util */

const $ = (sel, root = document) => root.querySelector(sel);
const el = (tag, attrs = {}, ...kids) => {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null || v === undefined || v === false) continue;
    if (k === 'class') n.className = v;
    else if (k === 'html') n.innerHTML = v;
    else if (k.startsWith('on')) n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v);
  }
  for (const kid of kids.flat()) {
    if (kid === null || kid === undefined || kid === false) continue;
    // appendChild needs a Node: strings must become text nodes first.
    n.appendChild(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  }
  return n;
};
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) =>
  ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const fmtTime = (s) => !s ? '—' : new Date(s).toLocaleString(undefined,
  { month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' });
const ago = (s) => {
  if (!s) return 'never';
  const d = (Date.now() - new Date(s).getTime()) / 1000;
  if (d < 60) return `${Math.max(0, Math.round(d))}s ago`;
  if (d < 3600) return `${Math.round(d / 60)}m ago`;
  if (d < 86400) return `${Math.round(d / 3600)}h ago`;
  return `${Math.round(d / 86400)}d ago`;
};
const bytes = (n) => !n ? '0 B' : n < 1024 ? `${n} B` : `${(n / 1024).toFixed(1)} KB`;

function toast(msg, kind = '') {
  let box = $('.toast');
  if (!box) { box = el('div', { class: 'toast' }); document.body.appendChild(box); }
  const t = el('div', { class: kind }, msg);
  box.appendChild(t);
  setTimeout(() => t.remove(), 4200);
}

async function api(path, { method = 'GET', body, raw } = {}) {
  const res = await fetch(API + path, {
    method,
    headers: {
      ...(state.token ? { Authorization: `Bearer ${state.token}` } : {}),
      ...(body ? { 'Content-Type': 'application/json' } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (raw) return res;
  const text = await res.text();
  let data = {};
  try { data = text ? JSON.parse(text) : {}; } catch { data = { raw: text }; }
  if (!res.ok) {
    const code = data?.error?.code || res.status;
    const msg = data?.error?.message || text || res.statusText;
    const err = new Error(`${code}: ${msg}`);
    err.status = res.status;
    err.code = code;
    throw err;
  }
  return data;
}

/* ------------------------------------------------------------------ login */

function renderLogin() {
  const wrap = el('div', { class: 'login-wrap' },
    el('form', {
      class: 'login',
      onsubmit: async (e) => {
        e.preventDefault();
        const input = $('#tok');
        const btn = $('#go');
        btn.disabled = true;
        btn.textContent = 'Checking…';
        state.token = input.value.trim();
        try {
          const view = await api('/printers/status');
          state.role = guessRole(view);
          state.client = state.client || 'operator';
          localStorage.setItem(STORE.token, state.token);
          start();
        } catch (err) {
          state.token = '';
          toast(err.message, 'bad');
          btn.disabled = false;
          btn.textContent = 'Sign in';
        }
      },
    },
      el('h1', {}, 'Cloud Print Service'),
      el('p', { class: 'sub' }, 'Operator panel — paste an admin token to sign in.'),
      el('input', {
        id: 'tok', name: 'token', type: 'password', autocomplete: 'off',
        placeholder: 'cps_live_…  or a bootstrap admin key', value: state.token, autofocus: true,
      }),
      el('div', { class: 'row', style: 'margin-top:14px' },
        el('button', { class: 'btn primary', id: 'go', type: 'submit' }, 'Sign in'),
        el('a', { class: 'muted', href: '/docs/', target: '_blank' }, 'API'),
      ),
      el('div', { class: 'hint' },
        'Issue one with ', el('code', {}, 'cpsctl token issue --client-id ops --role admin'), '.'),
    ));
  document.getElementById('app').replaceChildren(wrap);
}

function guessRole() { return 'admin'; } // any token that gets in is treated as operator

/* ------------------------------------------------------------------ shell */

const NAV = [
  ['overview', 'Overview'],
  ['printers', 'Printers'],
  ['jobs', 'Jobs'],
  ['tokens', 'Tokens'],
  ['preview', 'Preview'],
  ['ops', 'Ops'],
];

function shell() {
  const root = el('div', {});
  const bad = state.health === false;
  root.appendChild(el('div', { class: 'topbar' },
    el('div', { class: 'brand' },
      el('span', { class: `dot ${bad ? 'bad' : 'ok'}` }),
      'Cloud Print Service',
    ),
    el('nav', {}, NAV.map(([id, label]) => el('button', {
      class: state.route === id ? 'active' : '',
      onclick: () => go(id),
    }, label))),
    el('div', { class: 'spacer' }),
    el('span', { class: `pill ${state.live ? 'ok' : ''}`, id: 'livepill' },
      state.live ? 'live' : 'polling'),
    el('span', { class: 'pill', id: 'whoami' }, state.client || 'operator'),
    el('button', {
      class: 'icon', title: 'toggle theme', onclick: () => {
        const next = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
        document.documentElement.dataset.theme = next;
        localStorage.setItem(STORE.theme, next);
      },
    }, '◐'),
    el('button', { class: 'icon', title: 'sign out', onclick: signOut }, '⏻'),
  ));
  const main = el('main', { id: 'view' });
  root.appendChild(main);
  document.getElementById('app').replaceChildren(root);
  paint();
}

function go(route) { state.route = route; location.hash = route; paint(); }
function signOut() {
  localStorage.removeItem(STORE.token);
  state.token = ''; stopLive(); renderLogin();
}

window.addEventListener('hashchange', () => {
  const r = location.hash.replace('#', '');
  if (r && r !== state.route) { state.route = r; paint(); }
});

/* ------------------------------------------------------------------ live */

function start() {
  shell();
  refresh();
  setInterval(refresh, 5000);
  ensureLive();
  // Re-open the stream if it drops (proxy timeouts, printer restarts).
  setInterval(() => { if (!state.abort) ensureLive(); }, 10000);
  pollHealth();
  setInterval(pollHealth, 10000);
}

async function refresh() {
  try {
    const [fleet, jobs] = await Promise.all([
      api('/printers/status'),
      api('/jobs?limit=25').catch(() => ({ items: [] })),
    ]);
    state.printers = fleet.items || [];
    state.jobs = jobs.items || [];
    state.health = true;
  } catch (err) {
    state.health = false;
    if (err.status === 401) return signOut();
  }
  paint();
}

async function pollHealth() {
  try {
    const r = await fetch('/readyz');
    state.health = r.ok;
    const pill = $('#whoami');
    if (pill) pill.title = `readyz: ${r.status}`;
  } catch { state.health = false; }
}

/* SSE keeps the fleet view live; polling is the fallback and the source of truth.
 *
 * EventSource cannot set an Authorization header, so it would always 401 here. We
 * consume the stream with fetch() instead and parse the SSE frames by hand — which also
 * keeps the token out of the URL (no leaking into logs, proxies or Referer).
 */
async function startLive() {
  if (state.abort) return;
  state.abort = new AbortController();
  try {
    const res = await fetch(`${API}/events?topics=printer.status,job.state`, {
      headers: { Authorization: `Bearer ${state.token}`, Accept: 'text/event-stream' },
      signal: state.abort.signal,
    });
    if (!res.ok || !res.body) throw new Error(`stream ${res.status}`);
    setLive(true);
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buf = '';
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      let idx;
      while ((idx = buf.indexOf('\n\n')) >= 0) {
        const frame = buf.slice(0, idx);
        buf = buf.slice(idx + 2);
        const ev = /^event:\s*(.+)$/m.exec(frame);
        const name = ev ? ev[1].trim() : 'message';
        if (name === 'printer.status' || name === 'job.state') scheduleRefresh();
      }
    }
    setLive(false);
  } catch (err) {
    if (err.name !== 'AbortError') setLive(false);
  }
}
function setLive(on) {
  state.live = on;
  const pill = $('#livepill');
  if (pill) { pill.textContent = on ? 'live' : 'polling'; pill.className = `pill ${on ? 'ok' : ''}`; }
}
let refreshTimer = null;
function ensureLive() {
  if (!state.abort) startLive();
}
function scheduleRefresh() {
  if (refreshTimer) return;
  refreshTimer = setTimeout(() => { refreshTimer = null; refresh(); }, 250);
}
function stopLive() { state.abort?.abort(); state.abort = null; setLive(false); }

/* ------------------------------------------------------------------ views */

function paint() {
  const view = $('#view');
  if (!view) return;
  if (state.route === 'overview') return viewOverview(view);
  if (state.route === 'printers') return viewPrinters(view);
  if (state.route === 'jobs') return viewJobs(view);
  if (state.route === 'tokens') return viewTokens(view);
  if (state.route === 'preview') return viewPreview(view);
  if (state.route === 'ops') return viewOps(view);
  viewOverview(view);
}

function viewOverview(view) {
  const ps = state.printers;
  const usable = ps.filter((p) => p.usable).length;
  const broken = ps.filter((p) => !p.usable);
  const jobs = state.jobs;
  const recent = jobs.slice(0, 8);

  view.replaceChildren(
    el('section', {},
      el('div', { class: 'cards' },
        card('printers', ps.length, ''),
        card('usable', `${usable}/${ps.length}`, usable === ps.length ? 'ok' : (usable ? 'warn' : 'bad')),
        card('needs attention', broken.length, broken.length ? 'bad' : 'ok'),
        card('queued', ps.reduce((a, p) => a + (p.queue_depth || 0), 0), ''),
        card('recent jobs', jobs.length, ''),
      ),
    ),
    broken.length ? el('section', {},
      el('h2', {}, 'Needs attention'),
      el('div', { class: 'grid' }, broken.map(printerTile)),
    ) : null,
    el('section', {},
      el('h2', {}, ps.length - broken.length ? `Fleet · ${ps.length - broken.length} usable` : 'Fleet'),
      ps.filter((p) => p.usable).length
        ? el('div', { class: 'grid' }, ps.filter((p) => p.usable).map(printerTile))
        : el('div', { class: 'empty' }, broken.length ? 'No printer is usable right now.' : 'No printers registered.'),
    ),
    el('section', {},
      el('h2', {}, 'Recent jobs'),
      jobsTable(recent, false),
    ),
  );
}

function card(k, v, cls) {
  return el('div', { class: 'card' }, el('div', { class: 'k' }, k), el('div', { class: `v ${cls}` }, v));
}

function printerTile(p) {
  const st = p.status || {};
  return el('div', {
    class: 'tile', onclick: () => { state.selected = p.printer_id; go('printers'); },
  },
    el('div', { class: 'head' },
      el('div', {},
        el('div', { class: 'name' }, p.name || p.printer_id),
        el('div', { class: 'meta' }, `${p.printer_id} · ${p.site || '—'} · ${p.model || p.driver}`),
      ),
      el('span', { class: `state s-${st.state || 'unknown'}` }, st.state || 'unknown'),
    ),
    el('div', { class: 'foot' },
      el('span', { class: `pill ${p.usable ? 'ok' : 'bad'}` }, p.usable ? 'usable' : 'not usable'),
      st.queue_depth ? el('span', { class: 'pill' }, `queue ${st.queue_depth}`) : null,
      el('span', { class: 'pill' }, `seen ${ago(st.observed_at)}`),
      st.stale ? el('span', { class: 'pill bad' }, 'stale') : null,
    ),
  );
}

function viewPrinters(view) {
  const ps = state.printers;
  const sel = state.selected ? ps.find((p) => p.printer_id === state.selected) : null;
  const head = el('section', {},
    el('div', { class: 'toolbar' },
      el('h2', { style: 'margin:0;flex:1' }, sel ? `${sel.name || sel.printer_id}` : 'Printers'),
      state.selected ? el('button', { class: 'btn', onclick: () => { state.selected = null; paint(); } }, '← fleet') : null,
      el('button', { class: 'btn', onclick: probeAll }, 'Probe all'),
    ),
  );

  if (sel) {
    const st = sel.status || {};
    const jobs = state.jobs.filter((j) => j.printer_id === sel.printer_id);
    head.appendChild(el('div', { class: 'split' },
      el('div', {},
        el('div', { class: 'card' },
          el('h3', {}, 'Status'),
          el('dl', { class: 'kv' },
            el('dt', {}, 'state'), el('dd', {}, el('span', { class: `state s-${st.state}` }, st.state)),
            el('dt', {}, 'usable'), el('dd', {}, String(sel.usable)),
            el('dt', {}, 'raw'), el('dd', {}, st.raw || '—'),
            el('dt', {}, 'detail'), el('dd', {}, st.detail || '—'),
            el('dt', {}, 'observed'), el('dd', {}, `${fmtTime(st.observed_at)} (${ago(st.observed_at)})`),
            el('dt', {}, 'stale'), el('dd', {}, String(!!st.stale)),
            el('dt', {}, 'queue'), el('dd', {}, String(sel.queue_depth || 0)),
            el('dt', {}, 'host'), el('dd', {}, `${sel.host}:${sel.port}`),
            el('dt', {}, 'last job'), el('dd', {}, st.last_job
              ? `${st.last_job.state} ${st.last_job.job_id} · ${ago(st.last_job.at)}` : '—'),
          ),
        ),
      ),
      el('div', {},
        el('div', { class: 'card' },
          el('h3', {}, 'Actions'),
          el('div', { class: 'row' },
            el('button', { class: 'btn primary', onclick: () => testPrint(sel.printer_id) }, 'Test label'),
            el('button', { class: 'btn', onclick: () => probeOne(sel.printer_id) }, 'Probe now'),
            sel.enabled
              ? el('span', { class: 'pill ok' }, 'enabled')
              : el('span', { class: 'pill bad' }, 'disabled'),
          ),
          el('p', { class: 'muted', style: 'margin-bottom:0;font-size:12px' },
            'Enabling/disabling printers is config-driven (fleet changes go through review).'),
        ),
        el('div', { class: 'card', style: 'margin-top:14px' },
          el('h3', {}, 'Recent jobs'),
          jobsTable(jobs.slice(0, 8), false),
        ),
      ),
    ));
    view.replaceChildren(head);
    return;
  }

  head.appendChild(ps.length ? el('table', {},
    el('thead', {}, el('tr', {},
      el('th', {}, 'printer'), el('th', {}, 'site'), el('th', {}, 'model'),
      el('th', {}, 'state'), el('th', {}, 'usable'), el('th', {}, 'queue'),
      el('th', {}, 'observed'), el('th', {}, 'last job'),
    )),
    el('tbody', {}, ps.map((p) => {
      const st = p.status || {};
      return el('tr', { class: 'clickable', onclick: () => { state.selected = p.printer_id; paint(); } },
        el('td', {}, p.name || p.printer_id, el('div', { class: 'mono muted' }, p.printer_id)),
        el('td', {}, p.site || '—'),
        el('td', { class: 'mono' }, p.model || p.driver),
        el('td', {}, el('span', { class: `state s-${st.state}` }, st.state || 'unknown')),
        el('td', {}, el('span', { class: `pill ${p.usable ? 'ok' : 'bad'}` }, String(p.usable))),
        el('td', { class: 'mono' }, String(sel0(p))),
        el('td', { class: 'mono muted' }, ago(st.observed_at)),
        el('td', { class: 'mono muted' }, st.last_job ? st.last_job.state : '—'),
      );
    })),
  ) : el('div', { class: 'empty' }, 'No printers registered.'));
  view.replaceChildren(head);
}
const sel0 = (p) => p.queue_depth || 0;

function viewJobs(view) {
  const f = state.filter;
  const rows = state.jobs.filter((j) =>
    (!f.state || j.state === f.state) && (!f.printer || j.printer_id === f.printer));
  view.replaceChildren(
    el('section', {},
      el('div', { class: 'toolbar' },
        el('h2', { style: 'margin:0;flex:1' }, 'Jobs'),
        el('select', {
          onchange: (e) => { f.state = e.target.value; paint(); },
        }, ['', 'queued', 'sending', 'printed', 'sent_unknown', 'failed', 'cancelled']
          .map((s) => el('option', { value: s, selected: f.state === s }, s || 'any state'))),
        el('select', {
          onchange: (e) => { f.printer = e.target.value; paint(); },
        }, ['', ...state.printers.map((p) => p.printer_id)]
          .map((p) => el('option', { value: p, selected: f.printer === p }, p || 'any printer'))),
        el('button', { class: 'btn', onclick: refresh }, 'Refresh'),
      ),
      jobsTable(rows, true),
    ),
  );
}

function jobsTable(jobs, cancellable) {
  if (!jobs.length) return el('div', { class: 'empty' }, 'No jobs yet.');
  return el('table', {},
    el('thead', {}, el('tr', {},
      el('th', {}, 'job'), el('th', {}, 'printer'), el('th', {}, 'state'),
      el('th', {}, 'client'), el('th', {}, 'doc'), el('th', {}, 'size'),
      el('th', {}, 'queued'), el('th', {}, ''),
    )),
    el('tbody', {}, jobs.map((j) => el('tr', {},
      el('td', { class: 'mono' }, j.job_id),
      el('td', { class: 'mono' }, j.printer_id),
      el('td', {}, el('span', { class: `state s-${j.state}` }, j.state),
        j.attempts > 1 ? el('span', { class: 'pill', style: 'margin-left:6px' }, `${j.attempts} tries`) : null,
        j.partial_write ? el('span', { class: 'pill bad', style: 'margin-left:6px' }, 'partial') : null),
      el('td', { class: 'mono muted' }, j.client_id),
      el('td', { class: 'mono' }, j.biz_ref || j.external_id || '—'),
      el('td', { class: 'mono muted' }, bytes(j.payload_bytes)),
      el('td', { class: 'mono muted' }, ago(j.queued_at)),
      el('td', { class: 'right' },
        cancellable && ['queued', 'sending'].includes(j.state)
          ? el('button', { class: 'btn danger', onclick: () => cancelJob(j.job_id) }, 'cancel')
          : j.last_error ? el('span', { class: 'muted', title: j.last_error }, '…') : null),
    ))),
  );
}

async function cancelJob(id) {
  try { await api(`/jobs/${id}/cancel`, { method: 'POST' }); toast(`cancelled ${id}`, 'ok'); refresh(); }
  catch (e) { toast(e.message, 'bad'); }
}

async function testPrint(id) {
  try {
    const r = await api(`/printers/${id}/test`, { method: 'POST' });
    toast(`test label queued: ${r.job_id}`, 'ok');
    setTimeout(refresh, 700);
  } catch (e) { toast(e.message, 'bad'); }
}
async function probeOne(id) {
  try { await api(`/printers/probe`, { method: 'POST' }); toast('probed', 'ok'); setTimeout(refresh, 600); }
  catch (e) { toast(e.message, 'bad'); }
}
async function probeAll() { await probeOne('*'); }

function viewTokens(view) {
  view.replaceChildren(
    el('section', {},
      el('h2', {}, 'Issue a token'),
      el('form', {
        class: 'card', onsubmit: async (e) => {
          e.preventDefault();
          const client = $('#tk-client').value.trim();
          if (!client) return toast('client_id is required', 'bad');
          try {
            const body = { client_id: client, role: $('#tk-role').value, ttl: $('#tk-ttl').value || undefined };
            const note = $('#tk-note').value.trim();
            if (note) body.note = note;
            const r = await api('/admin/tokens', { method: 'POST', body });
            showSecret(r);
            loadTokens();
          } catch (err) { toast(err.message, 'bad'); }
        },
      },
        el('div', { class: 'row' },
          el('input', { id: 'tk-client', placeholder: 'client_id (wms)', style: 'flex:1;min-width:150px' }),
          el('select', { id: 'tk-role' }, el('option', { value: 'client' }, 'client'),
            el('option', { value: 'admin' }, 'admin')),
          el('input', { id: 'tk-ttl', placeholder: 'ttl (90d)', style: 'width:110px' }),
          el('input', { id: 'tk-note', placeholder: 'note', style: 'flex:1;min-width:130px' }),
          el('button', { class: 'btn primary', type: 'submit' }, 'Issue'),
        ),
        el('p', { class: 'muted', style: 'margin:10px 0 0;font-size:12px' },
          'The secret is shown once and stored only as a SHA-256 digest.'),
      ),
      el('div', { id: 'secret-slot' }),
    ),
    el('section', {},
      el('div', { class: 'toolbar' },
        el('h2', { style: 'margin:0;flex:1' }, 'Issued tokens'),
        el('button', { class: 'btn', onclick: loadTokens }, 'Refresh'),
      ),
      el('div', { id: 'tokens-table' }, el('div', { class: 'empty' }, 'loading…')),
    ),
  );
  // After the DOM exists: loadTokens() bails when its container is not mounted yet.
  loadTokens();
}

function showSecret(r) {
  const slot = $('#secret-slot');
  if (!slot) return;
  slot.replaceChildren(el('div', { class: 'card', style: 'border-color:var(--ok)' },
    el('h3', {}, `secret for ${r.client_id} — copy it now`),
    el('div', { class: 'secret' }, r.token),
    el('div', { class: 'row' },
      el('button', {
        class: 'btn', onclick: () => {
          navigator.clipboard?.writeText(r.token);
          toast('copied', 'ok');
        },
      }, 'Copy'),
      el('span', { class: 'muted mono' }, `id ${r.id} · expires ${fmtTime(r.expires_at)}`),
    ),
  ));
}

async function loadTokens() {
  const box = $('#tokens-table');
  if (!box) return;
  try {
    const r = await api('/admin/tokens');
    state.tokens = r.items || [];
    if (!state.tokens.length) {
      box.replaceChildren(el('div', { class: 'empty' },
        'No tokens issued yet. Restarting the service clears them (memory store).'));
      return;
    }
    box.replaceChildren(el('table', {},
      el('thead', {}, el('tr', {},
        el('th', {}, 'id'), el('th', {}, 'client'), el('th', {}, 'role'), el('th', {}, 'prefix'),
        el('th', {}, 'expires'), el('th', {}, 'status'), el('th', {}, 'uses'), el('th', {}, ''),
      )),
      el('tbody', {}, state.tokens.map((t) => el('tr', {},
        el('td', { class: 'mono' }, t.id),
        el('td', { class: 'mono' }, t.client_id),
        el('td', {}, t.role),
        el('td', { class: 'mono muted' }, `cps_live_${t.prefix}…`),
        el('td', { class: 'mono muted' }, t.expires_at ? fmtTime(t.expires_at) : 'never'),
        el('td', {}, el('span', {
          class: `pill ${t.revoked_at ? 'bad' : (!t.expires_at || new Date(t.expires_at) > new Date() ? 'ok' : 'bad')}`,
        }, t.revoked_at ? 'revoked' : (!t.expires_at || new Date(t.expires_at) > new Date() ? 'active' : 'expired'))),
        el('td', { class: 'mono muted' }, String(t.uses ?? 0)),
        el('td', { class: 'right' }, t.revoked_at ? null : el('div', { class: 'row', style: 'justify-content:flex-end' },
          el('button', { class: 'btn', onclick: () => rotate(t.id) }, 'rotate'),
          el('button', { class: 'btn danger', onclick: () => revoke(t.id) }, 'revoke'),
        )),
      ))),
    ));
  } catch (err) {
    box.replaceChildren(el('div', { class: 'empty' }, err.message));
  }
}

async function rotate(id) {
  try {
    const r = await api(`/admin/tokens/${id}/rotate`, { method: 'POST' });
    showSecret(r); loadTokens(); toast('rotated', 'ok');
  } catch (e) { toast(e.message, 'bad'); }
}
async function revoke(id) {
  if (!confirm(`Revoke ${id}? Anyone using it stops immediately.`)) return;
  try { await api(`/admin/tokens/${id}`, { method: 'DELETE' }); toast('revoked', 'ok'); loadTokens(); }
  catch (e) { toast(e.message, 'bad'); }
}

function viewPreview(view) {
  view.replaceChildren(
    el('section', {},
      el('h2', {}, 'Preview ZPL'),
      el('p', { class: 'muted', style: 'margin-top:-6px' },
        'Renders locally with the same engine the virtual printer uses — nothing is printed.'),
      el('textarea', { id: 'pv-zpl', placeholder: '^XA^FO40,40^A0N,40,40^FDHello^FS^XZ' },
        '^XA^CI28^PW812\n^FO40,30^A0N,44,44^FDOUTBOUND WH1^FS\n^FO40,90^GB732,3,3^FS\n^FO40,110^A0N,30,30^FDOrder OK-88213   Bin A-03^FS\n^FO40,150^A0N,26,26^FDSKU 12345 x2   Bay 7^FS\n^FO40,190^BCN,90,Y,N,N^FD1234567890123^FS\n^FO560,190^BQN,2,6^FDSKU12345^FS\n^FO560,380^GB180,90,3^FS\n^FO575,410^A0N,30,30^FDCHECKED^FS\n^XZ'),
      el('div', { class: 'toolbar', style: 'margin-top:10px' },
        el('input', { id: 'pv-w', type: 'number', value: '101.6', step: '0.1', style: 'width:100px' }),
        el('input', { id: 'pv-h', type: 'number', value: '152.4', step: '0.1', style: 'width:100px' }),
        el('input', { id: 'pv-d', type: 'number', value: '8', style: 'width:70px' }),
        el('span', { class: 'muted' }, 'mm × mm · dpmm'),
        el('button', { class: 'btn primary', onclick: doPreview }, 'Render'),
      ),
      el('div', { id: 'pv-out' }),
    ),
  );
}

async function doPreview() {
  const out = $('#pv-out');
  out.replaceChildren(el('div', { class: 'empty' }, 'rendering…'));
  try {
    const res = await api('/admin/preview', {
      method: 'POST',
      body: {
        zpl: $('#pv-zpl').value,
        width_mm: Number($('#pv-w').value),
        height_mm: Number($('#pv-h').value),
        dpmm: Number($('#pv-d').value),
      },
      raw: true, // the body is an image, not JSON
    });
    // The response body *is* the image; metadata comes back in headers.
    const h = res.headers;
    if (!res.ok) throw new Error(`render failed (${res.status})`);
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    const meta = `${h.get('X-Render-Backend') || '?'} · ${h.get('X-Render-Width-Dots')}×${h.get('X-Render-Height-Dots')} dots · ${h.get('X-Render-Duration-Ms')}ms`;
    const warnings = h.get('X-Render-Warnings');
    out.replaceChildren(el('div', { class: 'card' },
      el('div', { class: 'row', style: 'justify-content:space-between' },
        el('h3', {}, meta),
        el('a', { class: 'btn', href: url, download: `preview.${h.get('X-Render-Format') || 'png'}` }, 'Download'),
      ),
      el('img', { class: 'preview-img', src: url, alt: 'rendered label' }),
      warnings ? el('p', { class: 'muted' }, 'warnings: ' + warnings) : null,
    ));
  } catch (err) {
    out.replaceChildren(el('div', { class: 'empty' }, err.message));
  }
}

function viewOps(view) {
  view.replaceChildren(
    el('section', {},
      el('h2', {}, 'Ops'),
      el('div', { class: 'grid' },
        el('div', { class: 'card' },
          el('h3', {}, 'Endpoints'),
          el('dl', { class: 'kv' },
            el('dt', {}, 'health'), el('dd', {}, el('a', { href: '/healthz', target: '_blank' }, '/healthz')),
            el('dt', {}, 'ready'), el('dd', {}, el('a', { href: '/readyz', target: '_blank' }, '/readyz')),
            el('dt', {}, 'metrics'), el('dd', {}, el('a', { href: '/metrics', target: '_blank' }, '/metrics')),
            el('dt', {}, 'api'), el('dd', {}, API),
            el('dt', {}, 'events'), el('dd', {}, `${API}/events (SSE)`),
          ),
        ),
        el('div', { class: 'card' },
          el('h3', {}, 'Metrics'),
          el('pre', { class: 'mono muted', style: 'max-height:260px;overflow:auto;margin:0' },
            'loading…'),
        ),
      ),
    ),
  );
  fetch('/metrics').then((r) => r.text()).then((t) => {
    const pre = $('pre', $('#view'));
    if (pre) pre.textContent = t.split('\n').filter((l) => l.startsWith('cps_')).slice(0, 60).join('\n');
  }).catch(() => {});
}

/* ------------------------------------------------------------------ boot */

document.documentElement.dataset.theme = localStorage.getItem(STORE.theme) || 'dark';
state.route = location.hash.replace('#', '') || 'overview';
if (state.token) start(); else renderLogin();
