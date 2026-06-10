# caretd — Final Development Plan

`caretd` is a modern, **dynamic, multi-domain** SIP B2BUA and media server in Go. It is not an Asterisk clone — it removes Asterisk's biggest operational pain: static text config (`pjsip.conf`, `extensions.conf`). caretd has **no config files**. All config (SIP domains, endpoints, dialplan routes, trunks) lives in **PostgreSQL** and is read live. Config is added/changed/deleted with plain SQL (and therefore via APIs) — no edit-and-reload hell.

**caretd is a component, not the system of record.** It is embedded into a host application and **shares that application's PostgreSQL database**. The host app owns its own entities (users, tenants, billing, …). caretd must never collide with them and never claim to own them:

- caretd lives in its **own Postgres schema** (`caretd.*`). Zero table-name collision with the app.
- caretd has **no "tenant" table.** "Tenant" is the *app's* concept. caretd's unit of isolation is the **SIP domain**.
- The app maps each of its tenants onto one or more caretd **domains**. An optional opaque `scope` column on caretd rows lets the app tag config with its own reference (e.g. its tenant id) for bulk insert/delete — it is **not** a foreign key and caretd never constrains or owns it.
- Provisioning is just rows. When the app creates a tenant, it inserts the matching `caretd.*` rows **inside its own transaction** (same DB → atomic) — or calls caretd's control API. Either way: *one operation → config done*, no restart, no file.

This document supersedes `RAWPLAN.md` (kept as the original raw sketch). It is the authoritative roadmap: detailed scope, exact build steps, schema, and verifiable checkpoints per phase.

> Companion docs: `STYLEGUIDE.md` (binding engineering rules), `RAWPLAN.md` (origin sketch).

---

## 1. Product principles (what makes caretd "modern")

1. **Dynamic by default.** Zero static config files. Postgres is the source of truth. No reload-from-disk; config is queried/cached and refreshed via `LISTEN/NOTIFY`.
2. **A guest in the app's database.** caretd shares the host app's Postgres but isolates itself in the `caretd` schema. It owns no app entities and adds no tables outside its schema. Dropping the `caretd` schema fully uninstalls it.
3. **Multi-domain, not multi-tenant.** caretd's isolation boundary is the SIP domain. Many domains run live simultaneously. The app decides how its tenants map to domains; caretd is agnostic to that mapping (it only sees an opaque `scope` tag, if the app sets one).
4. **Config is rows, mutable anytime.** Add/change/delete config with SQL or the control API at runtime. The app can provision caretd atomically within its own tenant-creation transaction. Changes take effect live; established calls are unaffected.
5. **Control plane / data plane split.** caretd exposes a REST control API (config CRUD + runtime actions) and an observability API. SIP/RTP is the data plane.
6. **Observe-only dashboard.** `caretd-ui` is a separate service that reads the observability API. It never mutates engine state directly; any action it offers calls the control API.

---

## 2. Target architecture

```
   host application  ──owns──►  app tables (public schema): users, tenants, billing, ...
        │                          ▲
        │ provisions caretd        │  same database, separate schema
        │ rows in its own tx       │
        ▼                          │
                          ┌────────┼────────────────────────────────┐
                          │        │         caretd                  │
  SIP/RTP  ───────────►   │  ┌──────────┐ ┌──────────┐ ┌─────────────┐  │
  (data plane)            │  │Transport │→│Transaction│→│  Dialog     │  │
                          │  │UDP/TCP/TLS│ │  FSM     │ │  FSM        │  │
                          │  └──────────┘ └──────────┘ └─────────────┘  │
                          │  ┌──────────┐ ┌──────────┐ ┌─────────────┐  │
                          │  │Registrar │ │  B2BUA   │ │Media Engine │  │
                          │  │(per-dom) │ │ Leg A/B  │ │RTP/RTCP/SRTP│  │
                          │  └────┬─────┘ └────┬─────┘ └─────────────┘  │
                          │  ┌────▼────────────▼─────────────────────┐  │
                          │  │  Config Cache (per-domain, live)       │  │
                          │  │  ◄── LISTEN/NOTIFY caretd_config_changed│ │
                          │  └────────────────┬──────────────────────┘  │
                          │  ┌────────────┐   │   ┌──────────────────┐  │
   REST (control plane)──►│  │ Control API│   │   │ Observability API│◄─┼── caretd-ui
   (app / provisioning)   │  │ CRUD+actions│  │   │ state/metrics/SSE│  │   (separate,
                          │  └──────┬─────┘   │   └──────────────────┘  │   observe-only)
                          └─────────┼─────────┼────────────────────────┘
                                    │         │
                               ┌────▼─────────▼─────────────────┐
                               │   PostgreSQL (shared with app)  │
                               │   schema  caretd.*  (only this) │
                               │   domains/endpoints/routes/     │
                               │   trunks/registrations/cdrs     │
                               └─────────────────────────────────┘
```

Five logical planes:
- **Data plane** — SIP signaling + RTP media (`internal/sip,transport,transaction,dialog,b2bua,media`).
- **Config plane** — `caretd` schema stores + live per-domain cache (`internal/config,store`).
- **Control plane** — REST API for config CRUD + runtime actions (`internal/controlapi`).
- **Observability plane** — metrics + state + event stream (`internal/obs`).
- **UI** — `caretd-ui`, separate repo/service, observe-only.

### Package layout (extends STYLEGUIDE §1)

```
caretd/
├── cmd/caretd/main.go
├── internal/
│   ├── sip/ transport/ transaction/ dialog/ b2bua/ media/ clock/   # data plane
│   ├── domain/             # SIP-domain resolution (host → domain config), scope context
│   ├── store/              # Postgres stores behind interfaces (caretd schema; sqlc queries)
│   ├── config/             # live per-domain config cache + LISTEN/NOTIFY refresher
│   ├── controlapi/         # REST control plane (config CRUD, runtime actions)
│   ├── obs/                # metrics, state snapshots, SSE/WS event stream
│   └── migrate/            # embedded SQL migrations (all scoped to schema "caretd")
├── migrations/             # *.up.sql / *.down.sql — every object in schema caretd
├── docs/
└── testdata/
```

---

## 3. Tech decisions (locked)

| Area | Decision | Rationale |
|------|----------|-----------|
| Database | **PostgreSQL, shared with host app** | caretd is a guest; no separate DB to operate. `pgx` driver. |
| Isolation in DB | **Dedicated `caretd` schema** | No collision with app tables; `DROP SCHEMA caretd CASCADE` uninstalls cleanly. |
| Query layer | `sqlc` (typed queries) or `pgx` + hand-written | Compile-time-checked SQL, no ORM magic. All queries `search_path`-pinned to `caretd`. |
| Migrations | `goose` / `golang-migrate`, embedded via `go:embed` | Versioned; every object created **in schema `caretd`**, never `public`. |
| Config delivery | DB + in-memory cache + `LISTEN/NOTIFY` | Dynamic, no files, no restart, no dropped calls. |
| Isolation unit | **SIP domain** (not tenant) | caretd owns domains; app maps its tenants → domains. |
| App linkage | Opaque `scope TEXT` column (nullable) | App tags caretd rows with its own ref; **not a FK**; caretd never owns it. |
| Control plane | REST (chi/std `net/http`), JSON | Consumed by app/provisioning + `caretd-ui` later. |
| Observability | Prometheus metrics + JSON state API + SSE event stream | Standard, scrapeable, live-pushable. |
| Dashboard | `caretd-ui`, **separate service, observe-only**, frontend **TBD** | Decided later; API/data contract defined now (Phase 9). |
| Crypto/media | `pion` (SRTP/DTLS/transcode), stdlib `crypto/tls` | Never reimplement crypto (STYLEGUIDE §11). |

---

## 4. Multi-domain config model (caretd's isolation, NOT tenancy)

- **caretd owns SIP domains, nothing else.** A `caretd.domains` row is the isolation boundary. Endpoints, routes, trunks, registrations, CDRs all reference a `domain_id` within the `caretd` schema.
- **No tenant table, no FK into app tables.** The app's "tenant" is invisible to caretd. If the app wants to group caretd config by its tenant, it sets the opaque `scope` column (e.g. the app's tenant UUID as text). caretd treats `scope` as an uninterpreted label — useful for `DELETE FROM caretd.domains WHERE scope = $1` when the app deletes a tenant.
- **Resolution:** incoming SIP request → host of Request-URI / `To` domain → `caretd.domains.domain` → domain config. Trunk-originated calls resolve by the trunk's domain. Every handler runs inside a resolved domain context.
- **Isolation:** registrar bindings, dialplan, trunks, CDRs are queried `WHERE domain_id = $1`. No cross-domain leakage. SIP digest auth realm = the domain.
- **Provisioning (the "one operation"):** two equivalent paths, both atomic, both live, both no-restart:
  1. **App-side (recommended):** the app, inside its own tenant-creation transaction on the shared DB, inserts `caretd.domains` + `caretd.endpoints` + `caretd.routes`, then issues `NOTIFY caretd_config_changed, '<domain>'`. Same DB → the SIP config commits atomically with the app's tenant.
  2. **API-side:** `POST /v1/domains` does the same insert + NOTIFY for clients that don't share a DB session.

### caretd schema (all objects in schema `caretd`; built incrementally, full set by Phase 8)

```sql
CREATE SCHEMA IF NOT EXISTS caretd;
SET search_path TO caretd;

CREATE TABLE domains (                              -- caretd's isolation boundary
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  domain      TEXT NOT NULL UNIQUE,                 -- SIP domain, e.g. acme.sip.example.com
  scope       TEXT,                                 -- OPAQUE app reference (e.g. app tenant id); NOT a FK
  enabled     BOOLEAN NOT NULL DEFAULT true,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE endpoints (                            -- an AOR + auth identity, within a domain
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  domain_id    UUID NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
  username     TEXT NOT NULL,
  ha1          TEXT NOT NULL,                        -- MD5(user:realm:pass), never plaintext
  display_name TEXT,
  codecs       TEXT[],                               -- allowed, in preference order
  max_contacts INT NOT NULL DEFAULT 1,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (domain_id, username)
);

CREATE TABLE registrations (                         -- runtime AOR→contact bindings
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  domain_id   UUID NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
  endpoint_id UUID NOT NULL REFERENCES endpoints(id) ON DELETE CASCADE,
  contact     TEXT NOT NULL,
  transport   TEXT NOT NULL,                         -- udp|tcp|tls
  source_addr TEXT NOT NULL,                         -- observed src (NAT)
  user_agent  TEXT,
  expires_at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE routes (                                -- dynamic dialplan (replaces extensions.conf)
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  domain_id   UUID NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
  priority    INT NOT NULL,                          -- lower first
  match_regex TEXT NOT NULL,                         -- against dialed number/uri
  action      TEXT NOT NULL,                         -- dial|trunk|reject|voicemail
  target      TEXT NOT NULL,
  enabled     BOOLEAN NOT NULL DEFAULT true
);

CREATE TABLE trunks (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  domain_id   UUID NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  upstream    TEXT NOT NULL,                          -- sip:gw.provider.com
  register    BOOLEAN NOT NULL DEFAULT false,
  username    TEXT, secret TEXT,
  codecs      TEXT[],
  enabled     BOOLEAN NOT NULL DEFAULT true
);

CREATE TABLE cdrs (
  id          BIGSERIAL PRIMARY KEY,
  domain_id   UUID NOT NULL REFERENCES domains(id),
  scope       TEXT,                                   -- copied from domain for easy app-side reporting
  call_id     TEXT NOT NULL,
  from_uri    TEXT, to_uri TEXT,
  started_at  TIMESTAMPTZ, answered_at TIMESTAMPTZ, ended_at TIMESTAMPTZ,
  duration_s  INT, disposition TEXT,                  -- answered|busy|failed|no-answer
  codec       TEXT
);

CREATE TABLE api_keys (                               -- control/obs API auth (caretd-owned, optional)
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  domain_id   UUID REFERENCES domains(id) ON DELETE CASCADE,  -- NULL = admin/global
  key_hash    TEXT NOT NULL,
  scope_level TEXT NOT NULL DEFAULT 'domain',          -- admin|domain|readonly
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Indexes: `domains(domain)`, `domains(scope)`, `endpoints(domain_id,username)`, `registrations(domain_id,endpoint_id)`, `registrations(expires_at)`, `routes(domain_id,priority)`, `cdrs(scope)`.

> All FKs stay **within** the `caretd` schema. caretd never references `public.*` (app) tables. The only link to the app is the opaque `scope` string.

---

## 5. Phased roadmap

Each phase lists **Goal → Build → Checkpoint (exact, verifiable) → Exit test**. A phase is not done until its checkpoint passes. Per STYLEGUIDE: every FSM is a typed transition table, every timer uses the injected `Clock`, every blocking op takes `context.Context`, `go test ./... -race` is the gate.

> Dependency spine: Parser → Transport → Transaction FSM → **Persistence/Domains** → Registrar → Dialog → Media → B2BUA/dynamic-dialplan → Control API → Observability/UI → Secure transport → Trunks → Hardening. The Transaction FSM (Phase 3) gates the call layers.

---

### Phase 0 — Project foundation
**Goal:** Buildable skeleton, Postgres wired, `caretd` schema + migrations running, CI green. No SIP yet.

**Build**
- `go mod init`, package layout (§2), `cmd/caretd` entrypoint with config from **env/flags only** (DSN, listen addrs) — no app config files.
- Postgres connection (`pgx` pool) against the **shared DB**; `search_path` pinned to `caretd`; health check; embedded migrations create `SCHEMA caretd` + `domains/endpoints` on startup.
- `Clock` interface (real + fake), structured logging (`slog`) with trace-ID support, root `context` + signal handling.
- CI: `go build`, `go vet`, `gofmt -l`, `go test -race`, a Postgres service container.

**Checkpoint**
- `caretd` boots, connects to PG, creates/migrates the `caretd` schema **without touching `public`**, logs ready, exits cleanly on SIGTERM.
- `migrate up`/`down` both succeed; `down` leaves `public` untouched and can `DROP SCHEMA caretd`.
- CI passes with `-race`.

**Exit test:** point caretd at a DB that already has app tables in `public` → caretd creates only `caretd.*`, app tables untouched; stop → graceful, no leaked goroutines (`goleak`).

---

### Phase 1 — SIP message parser
**Goal:** Parse and serialize SIP messages correctly. Pure, no network.

**Build**
- Request/Response structs; parser for start-line, headers, body (state-function parser per STYLEGUIDE §8).
- Header models: Via, From, To, Contact, CSeq, Call-ID, Content-Type, Content-Length, Max-Forwards, Expires.
- Serializer (wire round-trip). Tolerate compact forms, case-insensitive names, folded lines; reject malformed with typed error.

**Checkpoint**
- `sip.Parse(raw)` exposes `msg.Via[0].Branch`, `msg.From.Tag`, etc.; `msg.Serialize()` round-trips byte-stable on golden fixtures.
- Malformed inputs return `ErrMalformedMessage`, never panic.

**Exit test:** table-driven + golden-fixture suite over real captured packets (valid, compact, folded, malformed, oversized) passes.

---

### Phase 2 — UDP transport
**Goal:** Send/receive SIP over UDP.

**Build**
- `Transport` interface (`Send`, `Packets() <-chan InboundPacket`, `Close`); `UDPTransport` read loop (message-per-datagram).
- Bounded outbound queue per remote; Via branch injection (`z9hG4bK` + random); record observed source addr (NAT).

**Checkpoint**
- Round-trips a REGISTER with a real softphone (Linphone/Zoiper) visible in Wireshark `sip` filter; server echoes a valid response.
- Read loop bounded; one goroutine owns the write side.

**Exit test:** integration test sends bytes to the UDP socket, asserts parsed inbound + correct outbound framing; `-race` clean.

---

### Phase 3 — Transaction layer (RFC 3261 §17) — *the boss fight*
**Goal:** All four transaction FSMs with full timer management.

**Build**
- INVITE client/server + non-INVITE client/server FSMs as **typed transition tables** (STYLEGUIDE §8 tables are the spec).
- Timer set T1/T2/T4, A–K via injected `Clock`; retransmission + timeout.
- Transaction matching: **Via branch + CSeq method**. Terminal-state cleanup (stop timers).

**Checkpoint**
- Every state × event cell from STYLEGUIDE §8 is implemented and tested, incl. illegal events (defined error/no-op).
- Retransmit counts and timer firing verified with the **fake clock** (no real sleeps).

**Exit test:** FSM matrix tests + timer simulations green; `-race` clean; no goroutine leaks across transaction lifecycle.

---

### Phase 4 — Persistence + Multi-domain Registrar
**Goal:** DB-backed, domain-scoped registration. First config-plane usage. **This is where "dynamic + multi-domain" becomes real.**

**Build**
- `store` package: `pgx`/`sqlc` queries (schema `caretd`) for domains/endpoints/registrations behind interfaces.
- **Domain resolution:** request host → `caretd.domains` → domain config; domain context plumbed into handlers.
- REGISTER handler: digest auth (HA1 from `endpoints`, realm = domain, RFC 3261 §22), `Expires` processing, `*` wildcard removal, 200 OK with current bindings; OPTIONS liveness.
- Registration store writes to `caretd.registrations` (or in-memory hot path keyed by domain with periodic DB sync — see Hardening); expiry sweeper.
- **Config cache + `LISTEN/NOTIFY`:** in-memory per-domain endpoint cache, refreshed on `caretd_config_changed`.

**Checkpoint**
- Two endpoints under **different domains** register concurrently; bindings are isolated (no cross-domain visibility).
- Inserting an endpoint row + `NOTIFY` (or `POST` later) makes it registrable **without restart**.
- Wrong-realm / bad-credential REGISTER → 401/403.

**Exit test:** insert caretd rows via raw SQL (simulating the app's own transaction), register a softphone, assert binding + domain isolation; flip a credential in DB, assert live effect.

---

### Phase 5 — Dialog layer + basic call (signaling only)
**Goal:** Two endpoints in the same domain call each other. No media yet.

**Build**
- Dialog FSM `Early → Confirmed → Terminated` (STYLEGUIDE §8); dialog key = Call-ID + From-tag + To-tag.
- INVITE → 100 → 180 → 200 → ACK; BYE teardown; re-INVITE (hold/resume).
- Domain-scoped target lookup (callee registration within same domain).

**Checkpoint**
- Alice calls Bob (same domain); full INVITE→ACK→BYE flow matches the ladder in `RAWPLAN.md`; cross-domain call attempt is rejected (unless an explicit route allows it).

**Exit test:** two softphones complete a signaling-only call; dialog state transitions logged; teardown clean.

---

### Phase 6 — SDP + RTP media plane
**Goal:** Audio flows.

**Build**
- SDP parser/builder (RFC 4566); offer/answer pipeline (STYLEGUIDE §9, RFC 3264) constrained by each endpoint's `codecs`.
- RTP struct (RFC 3550); UDP RTP relay (no transcode yet) with pooled buffers; basic RTCP SR/RR for loss/jitter stats.
- Codec selection = intersection, caller order wins.

**Checkpoint**
- Alice↔Bob hear each other; selected codec respects endpoint `codecs` config; RTCP stats collected per call.

**Exit test:** real two-way audio call; RTP forwarded; jitter/loss surfaced in logs/metrics.

---

### Phase 7 — B2BUA + dynamic dialplan (no static files)
**Goal:** Logic-routed calls using **DB-driven** dialplan. Replaces `extensions.conf`.

**Build**
- B2BUA Leg A/Leg B (mediator pattern, STYLEGUIDE §7); independent dialogs per leg; SDP rewrite between legs.
- Dialplan engine reads `routes` from config cache (per domain, ordered by `priority`, regex match); actions: dial / trunk / reject.
- Call features: REFER transfer, hold via re-INVITE `a=sendonly`.

**Checkpoint**
- A call routes per `routes` rows; editing a route in DB + `NOTIFY` changes routing **on the next call** with no restart and no drop of in-progress calls.
- Per-leg codec/transport differ correctly.

**Exit test:** insert/modify route via SQL → observe routing change live; transfer + hold work.

---

### Phase 8 — Control-plane REST API + one-operation provisioning
**Goal:** caretd exposes the control API. **A domain plus its endpoints/routes can be created in one call/transaction.** This is the "dynamic by default" payoff.

**Build**
- HTTP server (separate listener/port from SIP). REST resources scoped to the `caretd` schema: `domains`, `endpoints`, `routes`, `trunks`, `api_keys`; runtime actions: `GET /v1/calls`, `DELETE /v1/calls/{id}` (kick), `GET /v1/registrations`.
- `POST /v1/domains` = single transaction: insert domain (+ optional `scope`) + endpoints + default route → `NOTIFY caretd_config_changed` → live.
- **Document the app-side path equivalently:** the host app may instead insert the same `caretd.*` rows inside its own tenant-creation transaction and `NOTIFY` — no API hop, fully atomic with the app's tenant. Provide a tested SQL recipe for this.
- Auth: API key (hashed) per `api_keys.scope_level` (admin/domain/readonly); domain-scoped keys can only touch their own rows.
- OpenAPI spec committed (contract for app/provisioning + future `caretd-ui`).

**Checkpoint**
- One `POST /v1/domains` (or one app-side SQL transaction) with a domain + 2 endpoints → those endpoints can REGISTER and call **immediately**, no restart. *(One operation → config done.)*
- `DELETE FROM caretd.domains WHERE scope = $1` (app deleting a tenant) removes all its SIP config and drops affected registrations cleanly.
- Domain-scoped key cannot read/write another domain's rows (403).

**Exit test:** end-to-end via **both** paths (API + raw app-style SQL tx) — provision → register → call → kick; then bulk-delete by `scope`; authz isolation tests pass.

---

### Phase 9 — Observability API + `caretd-ui` (separate, observe-only)
**Goal:** Make the instance easy to observe. caretd exposes observability; `caretd-ui` consumes it.

**Build (caretd side)**
- Prometheus `/metrics`: active calls, registrations/domain, transaction counts, RTP loss/jitter, dropped packets, FSM state gauges. Where useful, label by `scope` so the app can slice metrics by its own tenant.
- State API (JSON, read-only): `GET /obs/overview` (instance summary), `/obs/domains/{id}` (per-domain calls/regs), `/obs/calls`, `/obs/calls/{id}` (legs, codecs, RTP stats, dialog/txn state).
- Live event stream: **SSE** (or WS) `/obs/events` — call started/answered/ended, registration added/expired, FSM transitions. Per-domain filterable; auth via readonly key.
- Stable JSON schemas for all of the above = the `caretd-ui` data contract.

**Build (`caretd-ui` side — separate repo/service)**
- Service that consumes the observability API + SSE; renders instance state, per-domain analytics, live call list, registration table, RTP health.
- **Frontend stack deferred** (Go+htmx vs SPA decided at this phase). Define the data contract + screens now; pick rendering tech when starting the service.
- **Observe-only:** no engine mutation. Any "action" buttons (kick call) call the existing control API, not a UI-private path.

**Checkpoint**
- `/metrics` scrapeable by Prometheus; key series present (incl. `scope` labels where defined).
- `caretd-ui` shows live calls/registrations and updates within ~1s of an SSE event.
- Observability endpoints are read-only and domain-scoped by key.

**Exit test:** place a call → `caretd-ui` reflects it live (appears, answers, ends); metrics increment; readonly key cannot mutate.

---

### Phase 10 — Secure transport (TCP + TLS + SRTP)
**Goal:** Encrypted signaling and media.

**Build**
- `TCPTransport` (persistent conns, Content-Length framing); `TLSTransport` (`crypto/tls`); SIPS URIs.
- Cert management (self-signed dev + ACME/Let's Encrypt prod).
- SRTP via `github.com/pion/srtp` (DTLS-SRTP keying); per-endpoint/domain transport policy in config.

**Checkpoint**
- A SIPS endpoint registers and calls over TLS; media is SRTP; transport choice honors domain/endpoint config.

**Exit test:** TLS softphone end-to-end encrypted call; Wireshark confirms TLS + SRTP.

---

### Phase 11 — PSTN trunks + transcoding + DTMF + CDR
**Goal:** Connect to upstream SIP trunks; bridge codecs; record calls.

**Build**
- Outbound trunk registration (caretd registers upstream per `trunks` rows).
- Transcoding bridge (`pion` ecosystem) when trunk/endpoint codecs disagree (e.g. opus↔PCMU).
- DTMF relay: RFC 2833 RTP events ↔ SIP INFO.
- Trunk failover/load-balancing; **CDR writes** to `caretd.cdrs` per leg on call end (copy `scope` from the domain for easy app-side reporting/joins).

**Checkpoint**
- A call routes to a trunk (`routes.action='trunk'`); transcoding works across codec mismatch; CDR row written with correct disposition/duration + `scope`; failover to a second trunk on primary failure.

**Exit test:** call out via trunk to a real provider/test gateway; CDR + DTMF verified; kill primary trunk → failover.

---

### Phase 12 — Production hardening
**Goal:** Real load, failure, and operations.

**Build**
- Hot path scaling: optional Redis for distributed registration/call state across nodes (multi-instance); sticky/shared registrar.
- Graceful shutdown drains active calls (stop new INVITEs, let dialogs finish/timeout) — already scaffolded, now proven under load.
- Config hot-reload proven: bulk domain changes via API or app SQL never drop established calls.
- Full Prometheus dashboards + alerts; structured logs with call trace IDs end-to-end; load test (e.g. SIPp) for N concurrent calls/registrations.
- Backups/retention for `caretd.cdrs`; DB connection-pool tuning (share-aware so caretd never starves the app's pool); rate limiting on control API.

**Checkpoint**
- Sustained target load (define N) with bounded memory/goroutines; zero data races; graceful drain verified; multi-node registration consistent (if Redis enabled); caretd's DB usage stays within its allotted pool without impacting the host app.

**Exit test:** SIPp load profile passes at target concurrency; chaos test (kill a node, rotate config) with no dropped established calls.

---

## 6. Cross-cutting checkpoints (every phase)

- [ ] `go build ./... && go vet ./... && gofmt -l` clean
- [ ] `go test ./... -race -count=1` green; new FSM/pipeline behavior covered (STYLEGUIDE §8/§9/§10)
- [ ] No new static config file introduced — all config flows through DB + control API
- [ ] **Every new DB object is created in schema `caretd`** — never `public`; no FK into app tables
- [ ] Every new caretd table is **domain-scoped** (`domain_id`) + isolation test; carry opaque `scope` where app-side reporting helps
- [ ] Config changes take effect via `NOTIFY`/cache without restart
- [ ] No goroutine leaks (`goleak`); all blocking ops take `context.Context`
- [ ] Observability updated: any new runtime concept emits a metric and/or state-API field

---

## 7. Library strategy

| Layer | DIY | Library |
|-------|-----|---------|
| SIP parser, transaction FSM, dialog FSM, SDP, RTP relay, dialplan | ✅ write it | |
| Postgres | | `jackc/pgx`, queries via `sqlc` (search_path = caretd) |
| Migrations | | `goose` / `golang-migrate` (embedded; schema caretd) |
| HTTP control/obs API | | `net/http` + `chi` |
| SRTP / DTLS / transcoding | | `github.com/pion/*` |
| TLS | stdlib `crypto/tls` | |
| Metrics | | `prometheus/client_golang` |
| Load testing | | SIPp |

---

## 8. Timeline (indicative)

| Phase | Effort | Milestone |
|-------|--------|-----------|
| 0 | 3–5 days | Boots, shared-DB `caretd` schema + migrations, CI green |
| 1–2 | 1–2 weeks | Parse + UDP, Wireshark-validated |
| 3 | 2–3 weeks | Transaction FSMs (the boss fight) |
| 4 | 1–2 weeks | Multi-domain DB-backed registration |
| 5 | 1–2 weeks | First signaling call |
| 6 | 2–3 weeks | First call with audio |
| 7 | 2 weeks | Dynamic dialplan B2BUA |
| 8 | 1–2 weeks | Control API + one-operation provisioning |
| 9 | 2 weeks | Observability + caretd-ui |
| 10–11 | 1–2 months | Secure transport + trunks |
| 12 | ongoing | Production |

---

## 9. References

- RFC 3261 (SIP), 3550 (RTP), 4566 (SDP), 3264 (offer/answer), 2833 (DTMF), 3262 (PRACK, later).
- "SIP: Understanding the Session Initiation Protocol" — Alan Johnston.
- Tools: Wireshark (`sip`/`rtp` filters), Linphone/Zoiper (test clients), SIPp (load).

The Transaction FSM (Phase 3) remains the hardest layer. caretd being a **schema-isolated guest in the app's database** with **SIP domains** (not tenants) as its boundary, fully **SQL/API-driven and file-free**, is what makes it *modern* rather than an Asterisk reimplementation.
