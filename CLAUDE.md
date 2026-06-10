# CLAUDE.md — caretd

`caretd` is a **dynamic, multi-domain** SIP B2BUA and media server in Go: signaling, media bridging, codec negotiation, RTP relay. A modern Asterisk alternative — **no static config files**; config lives in PostgreSQL, mutated via SQL or a control-plane REST API, read live (`LISTEN/NOTIFY`). caretd is a **guest in the host app's database**: it owns a dedicated `caretd` schema, has **no tenant table** (the app owns tenancy), and isolates by **SIP domain** with an opaque `scope` tag linking back to the app. A separate `caretd-ui` service observes it (observe-only). Built in phases per `docs/PLAN.md`.

---

## Agent configuration — MANDATORY

**The agent configuration MUST be followed at all times. This is binding, not advisory.** All non-trivial work routes through the specialized agents defined in `./.claude/agents/`. Do not bypass them for multi-file, multi-layer, or design-bearing work.

| Agent | Model | Role | Use for |
|-------|-------|------|---------|
| **orchestrator** | opus | Coordinator | Decompose & delegate any task spanning multiple files/layers/versions. Owns sequencing and integration. |
| **architect** | opus | Designer | Design before code: new layers, interfaces, FSMs, pipelines, concurrency models, package boundaries. |
| **implementer** | sonnet | Coder | Write Go against an architect contract. Does not invent architecture. |
| **tester** | sonnet | Test author | Table-driven tests, FSM state coverage, golden fixtures, fake-clock timer tests, `-race`. |
| **automator** | haiku | Mechanical work | Boilerplate, codegen (`stringer`), formatting, dep bumps, fixtures. No judgment calls. |

### Routing rules (always apply)

1. **Anything spanning >1 file or layer → start with `orchestrator`.** It decomposes and delegates.
2. **Design before code.** Net-new structure (interface, FSM, package, pipeline) → `architect` first, then `implementer`.
3. **Implementer never redesigns.** Missing/ambiguous contract → escalate to orchestrator/architect.
4. **New/changed code → `tester` before "done".** FSM/protocol behavior is not done until test-covered.
5. **Mechanical, deterministic work → `automator`.** It escalates anything needing judgment.
6. Respect the layer dependency order (transport → transaction → dialog → registrar/B2BUA → media → dialplan → trunk → hardening). The **transaction FSM (v0.3) gates everything above it.**

---

## Required reading (before any code)

- `docs/PLAN.md` — **authoritative roadmap**: 13 phases (0–12), architecture (5 planes), multi-tenant schema, control/observability APIs, exact checkpoints + exit tests. Supersedes `RAWPLAN.md`.
- `docs/RAWPLAN.md` — original raw sketch (historical; superseded by `PLAN.md`).
- `docs/STYLEGUIDE.md` — **binding** rules: layout, errors, concurrency, timers, design patterns, RFC 3261 §17 transaction FSMs, dialog FSM, message/SDP/call pipelines, testing, observability. **The core of the project.**

---

## Hard rules (enforced by all agents)

- **Never `panic` on network input.** Malformed packets get a SIP error response, never a crash.
- **Errors wrapped with `%w` and context; never dropped silently.**
- **`context.Context` on every blocking/network op.** No goroutine leaks.
- **`go test ./... -race` is the gate.** Concurrency code must pass `-race`.
- **Never call `time.Now()`/`time.After()` in FSM/transaction logic** — inject the `Clock` interface so timers are testable.
- **FSM state is a typed enum with an explicit transition table** — no stringly-typed state, no scattered `if state ==`.
- **Transaction key = Via branch + CSeq method.** Dialog key = Call-ID + From-tag + To-tag.
- **DIY** the SIP parser, transaction FSM, SDP, RTP relay. **Use `pion`** for SRTP/DTLS/codec crypto — never reimplement crypto.
- **Bound every queue/buffer/read loop.** Acyclic, one-way layer dependencies.

---

## RFC references

- RFC 3261 — SIP core (transactions §17, registrar §10, auth §22)
- RFC 3550 — RTP/RTCP
- RFC 4566 — SDP
- RFC 3264 — SDP offer/answer
- RFC 2833 — DTMF as RTP events

Cite the governing RFC section in design and protocol code.
