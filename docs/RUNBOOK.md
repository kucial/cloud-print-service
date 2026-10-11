# Runbook

Day-two operations for Cloud Print Service. Assume the service is running, config is in
version control, and you have an admin credential (`cpsctl`) or access to the panel.

---

## First actions when something is wrong

1. `cpsctl status` — is the service ready, and how many printers are registered?
2. `cpsctl printers` — which printers are not usable, and why?
3. `cpsctl agent list` — any packing PCs that stopped heart-beating?
4. `cpsctl discover list` — anything new on the network that should not be there?

The panel at `/ui/` shows all of this live (fleet cards, Needs attention, Agents,
Discovery) with a live event stream; it is the fastest way to watch a site recover.

## Common symptoms and what to do

### Labels are not printing, but the API accepts jobs

- The printer is probably `not usable`. `cpsctl printers` and look at the state column.
- `media_out` / `ribbon_out` / `head_error` → restock, or call the site.
- `offline` → the network path is down, or the printer is off. Check the site first.
- `disabled` → someone disabled it in config. That is on purpose; find out why.

### A label printed twice

That is the failure this service is built to prevent, so investigate before continuing:

- Did the *same* document go out twice? Check `GET /api/v1/jobs?external_id=…`. If two
  jobs share one `external_id`, the client's retry logic is broken (it must not re-issue
  with a different id). If one job has `replayed: true`, the retry was handled safely and
  the duplicate came from elsewhere.
- Is `sent_unknown` involved? That state means the bytes may have reached the printer but
  we could not prove it. A human must decide whether to reprint; the service will not.

### Jobs sit at `queued` forever

- Check the printer's queue: `cpsctl printers` shows `QUEUE`. If it is at
  `queue_size_per_printer`, the service is backpressured — callers get `503 queue_full`
  and the queue will drain when the printer recovers.
- The queue TTL is 5 minutes by default; jobs that wait longer fail with
  `queued_too_long`. That is deliberate: a stale label is worse than none.
- If the printer's state is fine but nothing moves, the worker may be stuck on a bad
  payload. Look at the job's attempts: `GET /api/v1/jobs/{id}/attempts`.

### The printer shows `sent_unknown`

Do not automatically reprint. The printer may have the label. Check the printer physically
(or the render preview if it is a virtual printer), then reprint only after confirming it
did not print. This state exists because blindly retrying a half-written label produces
two.

### A packing PC's printers vanished

- Check the agent: `cpsctl agent list`. If it is `stale` (last_seen old), the PC is down or
  the agent died. The printers go `unusable` as their status goes stale — they are not
  deleted, so they reappear when the PC comes back.
- If the agent was revoked (`cpsctl agent revoke` shows in the audit log), its printers are
  removed from the fleet and its queued jobs fail with "agent … was deregistered".

### A printer shows as a candidate but should not be

- Dismiss it: `cpsctl discover dismiss <device-key>`. It will not be offered again until
  it is seen again after the candidate TTL.
- Adoption is deliberate: nothing becomes a printer from being found on the network.

### The token for a business system leaked

- `cpsctl token list` shows issued credentials. Find the prefix and revoke it:
  `cpsctl token revoke <id>`. It stops working immediately.
- Rotate instead if the integration must keep working: `cpsctl token rotate <id>`.

### Upgrading

- The panel and the service ship together. After upgrading, hard-refresh the panel once
  (`Cache-Control: no-store` means a restart is enough, but a stale tab can hold an old
  app.js until it reloads).
- Agents report their version in every heartbeat; the panel's Agents view shows it, so a
  mixed-version rollout is visible, not a mystery.

## Operator checklist for a new site

1. One machine on the warehouse LAN, any OS.
2. `cp config.example.json config.json`; set `addr`, the admin `api_keys`, and the site's
   printers (or leave the fleet empty and use discovery).
3. `cpsctl token issue --client-id erp --ttl 90d` and hand the secret to the ERP team.
4. `docker compose up` (or run the binary). Confirm `/readyz`.
5. Dispatch one label with the ERP's test ZPL. Watch the panel.

## When to escalate

- Two labels for one document, **and** the job log shows no `replayed` → there is a bug in
  the dispatch path. Stop, keep the evidence (`GET /api/v1/jobs/{id}` and
  `/api/v1/jobs/{id}/attempts`), and open an issue.
- `sent_unknown` that persists across several jobs on a healthy-looking printer → the
  printer is not confirming, or the status socket is misaligned. Check the printer's
  Link-OS "host status" setting (M0).
- An agent that cannot enroll → the bootstrap token is single-use. Issue a fresh one.
