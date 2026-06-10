# caretd Style Guide

Binding rules for a SIP B2BUA + media server: a long-running, concurrent network daemon parsing untrusted input under load. These rules prevent the specific failures a SIP server hits — goroutine leaks, data races, unbounded buffers, crashes on malformed packets, timer bugs. Treat them as strict, not advisory.

Companion: `PLAN.md` (versioned roadmap). RFCs: 3261 (SIP), 3550 (RTP), 4566 (SDP), 3264 (offer/answer), 2833 (DTMF).

---

## 1. Layout & dependency direction

```
caretd/
├── cmd/caretd/main.go      # entrypoint: wire concrete deps, config, signals — ONLY place that wires concretes
├── internal/
│   ├── sip/                # parser/serializer, header models
│   ├── transport/          # UDP/TCP/TLS behind one interface
│   ├── transaction/        # RFC 3261 §17 FSMs + timers
│   ├── dialog/             # dialog FSM
│   ├── registrar/          # AOR → binding store
│   ├── b2bua/              # leg A/B bridging, dialplan
│   ├── media/              # SDP, RTP/RTCP relay, codec negotiation
│   └── clock/              # injectable time source
├── pkg/                    # only if meant for external import
└── testdata/               # captured SIP/SDP/RTP fixtures
```

- Use `internal/` aggressively. One package per layer.
- **Dependency direction is acyclic and one-way.** Transport never imports dialog; FSM never imports dialplan.
- Everything below `cmd/` depends on interfaces, never concrete types.

---

## 2. Errors

- **Return errors, never `panic` on network input.** A malformed packet must never crash the daemon. `panic` only for unrecoverable programmer bugs; `recover()` at the top of each connection goroutine, log with trace ID, drop that one message/dialog.
- **Wrap with `%w` + context:** `fmt.Errorf("parse Via header: %w", err)`.
- **Sentinel/typed errors** for branchable cases: `var ErrMalformedMessage = errors.New(...)`; caller uses `errors.Is`.
- **Never drop silently.** `_ = f()` only with a comment justifying it.

---

## 3. Concurrency (the core of the server)

- **`context.Context` is the first param of every blocking/network op,** propagated everywhere. It drives shutdown drain and transaction timeouts.
- **No goroutine leaks.** Every `go func()` has a defined exit tied to a context or closed channel. Test with `goleak`.
- **Ownership — pick one per component, document it:** channels transfer ownership of data between goroutines; mutex guards shared mutable state (e.g. registrar map). One goroutine owns each connection's **write** side — never write a conn from multiple goroutines.
- **The sender closes a channel, never the receiver.** Document the closer.
- **Bound everything** — every queue/channel has capacity, every read loop a max buffer. Unbounded = OOM under packet flood.
- **Worker pools, not unbounded spawning.** Bounded pool or per-dialog goroutine with lifecycle control.
- `go test ./... -race -count=1` is the gate. CI runs `-race`.

---

## 4. Time & timers (SIP lives or dies here)

- **Never call `time.Now()` / `time.After()` in FSM/transaction logic.** Inject a clock:
  ```go
  type Clock interface {
      Now() time.Time
      NewTimer(d time.Duration) Timer   // wraps time.Timer; fakeable
  }
  ```
  Real clock in prod, fake clock in tests → deterministic, instant timer tests.
- **Always `Stop()` timers** you create; drain the channel if needed.
- RFC 3261 timers are named config durations, never inline magic numbers (table in §7).

---

## 5. Interfaces & types

- **Accept interfaces, return concrete structs.**
- **Small interfaces (1–3 methods), defined at the consumer:**
  ```go
  type Transport interface {
      Send(ctx context.Context, addr net.Addr, msg []byte) error
      Packets() <-chan InboundPacket
      Close() error
  }
  ```
- **Make illegal states unrepresentable.** FSM state is a typed enum, never a string:
  ```go
  type TxnState int
  const ( StateCalling TxnState = iota; StateProceeding; StateCompleted; StateTerminated )
  ```
  Add `//go:generate stringer -type=TxnState` for readable logs.

---

## 6. Untrusted input

- Parsers tolerate real-world mess: case-insensitive header names, compact forms (`v:` = `Via:`), folded lines, stray whitespace.
- Reject malformed input with a typed error + proper SIP response (400/4xx). Never crash, never hang.
- Cap message size, header count, body length **before** allocating.
- Validate `Content-Length` against actual body; never trust the declared length.

---

## 7. Design patterns (this codebase)

Favor composition and small interfaces over inheritance hierarchies.

| Pattern | Where | Note |
|---------|-------|------|
| **Strategy** | `Transport` (UDP/TCP/TLS) | One interface, swappable impls. Add TLS in v0.8 without touching the FSM. |
| **State machine** | transactions, dialogs | Typed enum + explicit transition table. §8. |
| **State-function** | SIP/SDP parser | `stateFn func(*parser) stateFn` loop (Rob Pike lexer). |
| **Functional options** | server/component config | `WithClock`, `WithT1`, … defaults in constructor, then apply opts. |
| **DI via interfaces** | all layers | Wire concretes only in `main.go`; everything else takes interfaces. |
| **Registry/Store** | registrar AOR table | `BindingStore` interface. v0.4 `map+RWMutex` → v1.0 Redis/Postgres, no handler change. |
| **Mediator** | B2BUA bridge | Leg A/B never reference each other; `Bridge` routes events between them. |
| **Pipeline** | inbound msg, SDP, call setup | Ordered stages. §9. |
| **Object pool** | RTP buffers | `sync.Pool` of `[]byte` on the media hot path (thousands pkt/s/call). Measure first. |
| **Fan-out/fan-in** | media relay | One reader per RTP source fans out to writers; RTCP stats fan in per-call. Bounded channels. |
| **Context tree** | shutdown | Root ctx cancels on signal; transports/transactions/dialogs derive children; cancel propagates down. |

**Anti-patterns (banned):** god object (split by layer); stringly-typed state (`state == "ringing"`); shared mutable state with no documented owner; `time.Now()` in logic; premature generality (no plugin system before two impls exist).

```go
// Functional options
type ServerOption func(*Server)
func WithClock(c Clock) ServerOption { return func(s *Server) { s.clock = c } }
func NewServer(opts ...ServerOption) *Server {
    s := &Server{clock: RealClock{}, t1: 500 * time.Millisecond}
    for _, o := range opts { o(s) }
    return s
}

// Registrar store — swappable backend
type BindingStore interface {
    Put(aor string, b Binding) error
    Get(aor string) ([]Binding, error)
    Remove(aor, contact string) error
    Expire(now time.Time)
}

// RTP buffer pool
var rtpBufPool = sync.Pool{New: func() any { return make([]byte, 1500) }}
```

---

## 8. State machines (the hardest part — RFC 3261 §17)

Model every FSM as **(State, Event) → (NewState, Action)** through a single transition function. No `if state ==` scattered across the code — one place owns transitions. A table is auditable (every cell visible incl. illegal), testable (tester iterates the matrix), and makes illegal transitions explicit (defined error/no-op, never undefined).

```go
type State int
type Event int
type Action func(ctx context.Context) error
type transition struct { next State; action Action }
type table map[State]map[Event]transition

type Machine struct { mu sync.Mutex; state State; table table; clock Clock }

func (m *Machine) Fire(ctx context.Context, ev Event) error {
    m.mu.Lock()
    t, ok := m.table[m.state][ev]
    if !ok { m.mu.Unlock(); return fmt.Errorf("illegal event %v in state %v", ev, m.state) }
    prev := m.state; m.state = t.next; act := t.action
    m.mu.Unlock()
    slog.Debug("fsm transition", "from", prev, "event", ev, "to", t.next)
    if act != nil { return act(ctx) }
    return nil
}
```

Use the **table** form for protocol FSMs (transactions, dialogs); the **state-function** form for the parser.

**Transaction key = Via branch + CSeq method** (NOT Call-ID — that's dialog scope). Four FSMs:

### INVITE Client (UAC sending INVITE) — `Calling → Proceeding → Completed → Terminated`

| State | Event | Next | Action |
|-------|-------|------|--------|
| Calling | Timer A | Calling | retransmit INVITE; reschedule A (2×, cap T2) |
| Calling | Timer B | Terminated | inform TU: timeout |
| Calling | 1xx | Proceeding | pass to TU |
| Calling | 2xx | Terminated | pass to TU (ACK in TU) |
| Calling | 300–699 | Completed | send ACK; start Timer D |
| Proceeding | 1xx | Proceeding | pass to TU |
| Proceeding | 2xx | Terminated | pass to TU |
| Proceeding | 300–699 | Completed | send ACK; start Timer D |
| Completed | 300–699 | Completed | retransmit ACK |
| Completed | Timer D | Terminated | cleanup |

### INVITE Server (UAS receiving INVITE) — `Proceeding → Completed → Confirmed → Terminated`
- Proceeding + TU 2xx → Terminated.
- Proceeding + TU 300–699 → send response, start Timer G (retransmit) + H (timeout) → Completed.
- Completed + ACK → Confirmed; + Timer G → retransmit final; + Timer H → Terminated (no ACK).
- Confirmed + Timer I → Terminated.

### Non-INVITE Client/Server (REGISTER, OPTIONS, BYE, …) — `Trying → Proceeding → Completed → Terminated`
- Client: Timer E retransmit, Timer F timeout, Timer K cleanup.
- Server: Timer J cleanup.

### Timer reference

| Timer | Default | Meaning |
|-------|---------|---------|
| T1 | 500 ms | RTT estimate; base retransmit interval |
| T2 | 4 s | max retransmit interval |
| T4 | 5 s | max time a message lingers in network |
| A / B | T1 / 64·T1 | INVITE client retransmit (doubles) / timeout |
| D | ≥32 s | client wait for response retransmits |
| G / H / I | T1 / 64·T1 / T4 | INVITE server retransmit (doubles) / wait-for-ACK / wait-for-ACK-retransmits |
| E / F | T1 / 64·T1 | non-INVITE client retransmit / timeout |
| J / K | 64·T1 / T4 | non-INVITE server / client cleanup |

All timers go through the injected `Clock` — tests drive them with a fake clock, never real sleeps.

### Dialog FSM (v0.5) — `Early → Confirmed → Terminated`
- Early + 2xx → Confirmed; + 300–699 → Terminated.
- Confirmed + BYE → Terminated; + re-INVITE → renegotiate (hold/resume), stay Confirmed.
- **Dialog key = Call-ID + From-tag + To-tag.** A dialog owns its transactions; it does not duplicate transaction state.

---

## 9. Pipelines (multi-step operations)

A pipeline is a fixed ordered sequence of stages, each `func(ctx, in) (out, error)`, short-circuiting on error. Use it for deterministic sequences (vs. event-driven FSMs that loop on external input). **Pipeline = stateless routing; FSM = stateful conversation.** A pipeline's job ends by handing stateful messages to the owning FSM.

### Inbound message
```
raw bytes → decode (→400) → validate headers/Content-Length/Max-Forwards (→4xx)
→ match-txn (branch+method) → match-dialog (Call-ID+tags) → route (registrar/B2BUA/dialplan)
→ respond/forward
```
```go
func (p *Pipeline) Handle(ctx context.Context, raw []byte) error {
    msg, err := decode(ctx, raw)
    if err != nil { return p.reject(400, err) }
    if err := validate(ctx, msg); err != nil { return p.reject(statusFor(err), err) }
    txn := p.matchTxn(msg)
    return txn.Fire(ctx, eventFor(msg))   // hand off to the FSM
}
```

### SDP offer/answer (v0.6, RFC 3264)
```
offer → parse (m=,c=,a=rtpmap,a=fmtp) → extract-codecs (ordered/media)
→ intersect (caller order wins) → select (first common, else port 0) → build-answer → allocate-rtp
```
Example: Alice offers PCMU(0),PCMA(8),opus(111); we support PCMU,opus → select PCMU.

### Outbound call setup (B2BUA, v0.7) — pipeline orchestrating FSMs
```
incoming INVITE (Leg A) → auth/route → create-legA (Early, 100 Trying)
→ create-legB (INVITE → callee) → bridge-media (allocate relay, rewrite SDP)
→ await-answer (Leg B 200 → Leg A 200) → confirm (ACK both → Confirmed, media flows)
→ teardown (BYE either → BYE other → both Terminated)
```

### Pipeline rules
- Every stage takes `context.Context`; cancellation aborts remaining stages and cleans up anything allocated (RTP ports, half-open dialogs).
- Stage error maps to a SIP status: parse→400, auth→401/403, no route→404, timeout→408.
- Resource-allocating stages register `defer`/rollback so mid-pipeline failure leaks nothing.

---

## 10. Testing

- **Table-driven** for parsers/pure functions.
- **Golden fixtures** in `testdata/`: assert parse→serialize round-trips on real packets.
- **FSM coverage:** every state × event cell, including illegal events.
- **Fake clock** for all timer tests — never real sleeps.
- `go test ./... -race -count=1` (`-count=1` defeats the cache).

---

## 11. Dependencies

- **DIY:** SIP parser, transaction FSM, SDP, RTP relay (learning core; need full control).
- **Use `pion`:** SRTP, DTLS, codec transcoding — never reimplement crypto. Stdlib `crypto/tls` for TLS.
- `go mod tidy` clean; pin versions; minimal dependency surface.

---

## 12. Observability & lifecycle

- **Structured logging** (`log/slog`). Every call-path line carries a **trace ID** (Call-ID or derived span) so a call reconstructs across legs. FSM transitions at debug, protocol errors at warn, server faults at error.
- **Prometheus** from v1.0: active calls, registrations, transaction count, RTP loss/jitter, dropped packets.
- **Graceful shutdown:** trap SIGINT/SIGTERM → cancel root context → drain active calls (stop accepting new INVITEs, let dialogs finish/timeout) → close transports → exit. `defer`-close every conn/file/timer. Dialplan hot-reload without dropping established dialogs.

---

## Implementation checklist

**Every FSM:** typed State/Event enums (`stringer`) · complete transition table (every cell decided) · illegal events = defined error/no-op · timers via injected `Clock` · `Fire` goroutine-safe or single-goroutine-confined · terminal state cleans up · tester covers full matrix + every timer.

**Every pipeline:** ordered `func(ctx, in)(out, error)` stages · errors map to correct SIP status · resource stages roll back on later failure · context cancellation aborts + cleans up · stateful messages handed to the owning FSM (no duplicated state).
