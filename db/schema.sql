-- Cloud Print Service schema (Postgres 14+).
-- The MVP binary ships with an in-memory store; this is the production schema and the
-- contract the Postgres store implements. See docs/DESIGN.md §8.

create table if not exists printers (
  printer_id  text primary key,               -- 'wh1-pack-03': stable, human-readable
  name        text not null,
  site        text not null default '',
  model       text not null default '',
  driver      text not null,                  -- zebra9100 | tsc9100 | mock
  host         text not null,
  port         int  not null default 9100,
  enabled      boolean not null default true,
  remark          text not null default '',
  custom_label    text not null default '',
  paper_size      text not null default '',
  media_width_mm  numeric not null default 0,
  media_height_mm numeric not null default 0,
  created_at      timestamptz not null default now(),
  updated_at  timestamptz not null default now()
);
create index if not exists printers_site_enabled_idx on printers (site, enabled);

create table if not exists jobs (
  id              uuid primary key default gen_random_uuid(),
  client_id       text not null,
  external_id     text not null,              -- business id from ERP/WMS
  printer_id      text not null,
  state           text not null,              -- queued|sending|printed|sent_unknown|failed|cancelled
  payload         text not null,              -- MVP: inline ZPL. Later: object-storage URI
  payload_bytes   int  not null,
  payload_sha256  text not null,
  copies          int  not null default 1,
  page_copies     jsonb not null default '[]'::jsonb,
  priority        text not null default 'normal',
  biz_ref         text not null default '',
  meta            jsonb not null default '{}'::jsonb,
  attempts        int  not null default 0,
  last_error      text,
  partial_write   boolean not null default false,
  idempotency_key text,
  queued_at       timestamptz not null default now(),
  sent_at         timestamptz,
  finished_at     timestamptz,
  constraint jobs_client_external_uniq unique (client_id, external_id)
);
create index if not exists jobs_printer_queued_idx  on jobs (printer_id, queued_at desc);
create index if not exists jobs_client_queued_idx   on jobs (client_id, queued_at desc);
create index if not exists jobs_active_idx          on jobs (state) where state in ('queued','sending');

create table if not exists job_attempts (
  id          bigserial primary key,
  job_id      uuid not null references jobs(id) on delete cascade,
  attempt_no  int  not null,
  started_at  timestamptz not null default now(),
  finished_at timestamptz,
  result      text,                           -- ok|connect_error|write_error|partial_write|unconfirmed
  bytes_sent  int not null default 0,
  error       text
);
create index if not exists job_attempts_job_idx on job_attempts (job_id);

-- Issued credentials for business systems (docs/DESIGN.md §7.5). The plaintext secret is
-- returned exactly once at creation; only sha256(secret) is stored.
create table if not exists api_tokens (
  id           text primary key,             -- 'tok_7_4YET6CFB'
  key_hash     text not null unique,         -- sha256(secret)
  client_id    text not null,
  role         text not null default 'client',-- client | admin
  prefix       text not null,                -- first chars of the secret, display only
  note         text not null default '',     -- e.g. 'wms pilot, OPS-1234'
  created_at   timestamptz not null default now(),
  expires_at   timestamptz,
  revoked_at   timestamptz,
  last_used_at timestamptz,
  uses         int not null default 0,
  rate_rps     int
);
create index if not exists api_tokens_client_idx on api_tokens (client_id, created_at desc);
create index if not exists api_tokens_active_idx  on api_tokens (client_id) where revoked_at is null;

-- Status transition history: turns "the printer was flapping at 02:00" into a query.
create table if not exists printer_events (
  id         bigserial primary key,
  printer_id text not null,
  state      text not null,
  raw        text,
  at         timestamptz not null default now()
);
create index if not exists printer_events_idx on printer_events (printer_id, at desc);

-- Ops queries used by the sweeper / dashboards.
create or replace view job_throughput as
select printer_id,
       state,
       count(*)                                        as jobs,
       min(queued_at)                                  as first_at,
       max(queued_at)                                  as last_at,
       avg(extract(epoch from (coalesce(finished_at, now()) - queued_at))) as avg_lifetime_s
from jobs
where queued_at > now() - interval '7 days'
group by printer_id, state;
