# xphone — Specification v1.3

## Overview

`xphone` is a Go library that wraps a SIP Extension (UA) and exposes a clean, event-driven API for managing concurrent calls. It handles SIP signaling, registration lifecycle, RTP media, and call control — allowing any Go application to embed telephony capabilities without dealing with SIP/RTP internals.

**Stack:**
- SIP Signaling: `emiago/sipgo`
- RTP Media: `pion/rtp`
- Language: Go 1.22+

---

## Design Principles

1. **Embed, don't deploy** — `xphone` is a library, not a service. No extra process, no network hop.
2. **Event-driven** — The caller reacts to events via callbacks. No polling.
3. **Concurrency-safe** — Each call runs in its own goroutine. Shared state is protected internally.
4. **Media-agnostic** — The library exposes raw RTP packets and/or decoded PCM frames. The app decides what to do with them.
5. **Fail-loud** — Errors surface immediately via return values and error events. No silent failures.
6. **Testable** — Core interfaces are abstracted so they can be mocked in unit tests.

---

## Package Structure

```
xphone/
├── xphone.go          # Public API: Phone struct, config, lifecycle
├── call.go            # Call struct, state machine, control methods
├── media.go           # RTP session management, audio pipeline
├── registry.go        # SIP REGISTER, keepalive, re-registration
├── transport.go       # SIP transport abstraction (UDP/TCP/TLS)
├── dtmf.go            # DTMF detection and generation (RFC 4733)
├── errors.go          # Sentinel errors and error types
├── events.go          # Event type definitions
├── options.go         # Functional options pattern for config
├── internal/
│   ├── sdp/           # SDP offer/answer builder
│   ├── dialog/        # Dialog tracker (concurrent call map)
│   ├── media/
│   │   ├── jitterbuffer.go  # Jitter buffer implementation
│   │   ├── codec.go         # Codec encode/decode (PCMU, PCMA, G722, Opus)
│   │   └── rtpengine.go     # RTP session, port mgmt, timestamp handling
│   └── util/          # Logging, timers, helpers
└── testutil/
    ├── mock_phone.go  # Mock Phone for app-level testing
    └── mock_call.go   # Mock Call for unit testing
```

---

## Configuration

```go
type Config struct {
    // SIP Extension credentials
    Username  string
    Password  string
    Host      string        // PBX host
    Port      int           // default: 5060
    Transport string        // "udp" | "tcp" | "tls" — default: "udp"
    TLSConfig *tls.Config   // required if Transport == "tls"

    // Registration
    RegisterExpiry   time.Duration // default: 60s
    RegisterRetry    time.Duration // default: 5s
    RegisterMaxRetry int           // default: 10, 0 = unlimited

    // NAT
    NATKeepaliveInterval time.Duration // default: 25s (CRLF ping over UDP)

    // Media
    RTPPortMin   int           // default: 10000
    RTPPortMax   int           // default: 20000
    CodecPrefs   []Codec       // ordered preference list
    JitterBuffer time.Duration // default: 50ms
    MediaTimeout time.Duration // default: 30s — no RTP received → call dead
                               // suspended automatically while call is OnHold
    PCMFrameSize int           // samples per frame, default: 160 (20ms @ 8kHz)
    PCMRate      int           // output sample rate for PCMReader, default: 8000
                               // Opus (native 48kHz) is resampled to match PCMRate.
                               // Resampler uses bandlimited (sinc) interpolation.
                               // Set to 48000 if you need native Opus quality.

    // Logging
    Logger *slog.Logger // optional, defaults to slog.Default()
}

// CallState represents the current state of a call.
// OnHold is a distinct state from Active — not a flag.
type CallState int
const (
    StateIdle          CallState = iota
    StateRinging                 // inbound: INVITE received, not yet accepted
    StateDialing                 // outbound: INVITE sent, no response yet
    StateRemoteRinging           // outbound: 180 received
    StateEarlyMedia              // outbound: 183 received + WithEarlyMedia set
    StateActive                  // call established, RTP flowing
    StateOnHold                  // re-INVITE with a=sendonly/inactive sent or received
    StateEnded                   // terminal — BYE, CANCEL, Reject, or error
)

type Codec int
const (
    CodecPCMU Codec = 0   // G.711 µ-law (uLaw) — PT 0,  RTP clock: 8000Hz
    CodecPCMA Codec = 8   // G.711 a-law (aLaw) — PT 8,  RTP clock: 8000Hz
    CodecG722 Codec = 9   // G.722             — PT 9,  RTP clock: 8000Hz (16kHz audio)
    CodecOpus Codec = 111 // Opus              — PT 111 (dynamic), RTP clock: 48000Hz
)
```

### Functional options (alternative)

```go
phone, err := xphone.New(
    xphone.WithCredentials("1001", "secret", "pbx.example.com"),
    xphone.WithTransport("tls", tlsCfg),
    xphone.WithRTPPorts(10000, 20000),
    xphone.WithCodecs(xphone.CodecPCMU, xphone.CodecPCMA),
    xphone.WithJitterBuffer(50 * time.Millisecond),
    xphone.WithMediaTimeout(30 * time.Second),
    xphone.WithNATKeepalive(25 * time.Second),
    xphone.WithPCMRate(8000),
    xphone.WithLogger(myLogger),
)
```

---

## Core Interfaces

### Phone

```go
type Phone interface {
    Connect(ctx context.Context) error
    Disconnect() error
    Dial(ctx context.Context, target string, opts ...DialOption) (Call, error)
    OnIncoming(func(call Call))
    OnRegistered(func())
    OnUnregistered(func())
    OnError(func(err error))
    State() PhoneState
}
```

### DialOption

```go
type DialOptions struct {
    CallerID      string
    CustomHeaders map[string]string
    EarlyMedia    bool
    Timeout       time.Duration // default: 30s
                                // Effective dial timeout = min(Timeout, ctx deadline).
                                // If ctx is cancelled first, CANCEL is sent and ctx.Err() is returned.
                                // If Timeout fires first, CANCEL is sent and ErrDialTimeout is returned.
    CodecOverride []Codec
}

func WithCallerID(id string) DialOption
func WithHeader(name, value string) DialOption
func WithEarlyMedia() DialOption
func WithDialTimeout(d time.Duration) DialOption
func WithCodecOverride(codecs ...Codec) DialOption
```

Example:
```go
call, err := phone.Dial(ctx, "sip:1002@pbx",
    xphone.WithCallerID("Support"),
    xphone.WithHeader("X-Call-Source", "AI"),
    xphone.WithDialTimeout(20 * time.Second),
)
```

### Call

```go
type Call interface {
    // Identity
    ID() string           // internal unique ID (UUID)
    DialogID() string     // SIP dialog ID
    CallID() string       // SIP Call-ID header — use for PBX logs / SIP traces
    Direction() Direction // Inbound | Outbound
    RemoteURI() string
    RemoteIP() string
    RemotePort() int
    State() CallState

    // Negotiated media
    Codec() Codec // final negotiated codec after SDP exchange

    // SDP access (debugging / PBX interop diagnostics)
    LocalSDP() string
    RemoteSDP() string

    // Timing
    StartTime() time.Time    // set when call reaches Active
    Duration() time.Duration // 0 until Active

    // SIP Headers
    // Returned map is a copy — safe to read, mutations have no effect.
    Header(name string) []string
    Headers() map[string][]string

    // Control — inbound
    Accept(opts ...AcceptOption) error
    Reject(code int, reason string) error

    // Control — general
    // End() behavior:
    //   Dialing / RemoteRinging / EarlyMedia → sends CANCEL → OnEnded(EndedByCancelled)
    //   Active / OnHold                      → sends BYE   → OnEnded(EndedByLocal)
    End() error
    Hold() error
    Resume() error
    Mute() error
    Unmute() error
    SendDTMF(digit string) error // RFC 4733, digits: 0-9, *, #, A-D

    // Transfer
    BlindTransfer(target string) error
    // AttendedTransfer — deferred to v2

    // Media — inbound taps (always available, independent of each other)
    RTPRawReader() <-chan *rtp.Packet // pre-jitter: wire-accurate, may be out of order
    RTPReader() <-chan *rtp.Packet    // post-jitter: reordered, deduplicated

    // Media — outbound (mutually exclusive)
    // If RTPWriter is used, PCMWriter is ignored.
    // App using RTPWriter is responsible for correct codec framing,
    // timestamp increments, and payload format — the library does not validate these.
    RTPWriter() chan<- *rtp.Packet

    // Media — PCM (always decoded unless RTPWriter is active outbound)
    // Frame format: []int16, mono, PCMRate Hz, PCMFrameSize samples per frame.
    // PCMWriter expects frames paced at real-time rate (one frame per frame duration).
    // Frames arriving faster than real-time are queued up to channel buffer, then dropped (newest dropped).
    // RTPRawReader and RTPReader drop oldest packets on overflow.
    PCMReader() <-chan []int16
    PCMWriter() chan<- []int16

    // Events
    OnDTMF(func(digit string))
    OnHold(func())
    OnResume(func())
    OnMute(func())
    OnUnmute(func())
    OnMedia(func()) // fired when RTP session + decoder are live (post-SDP answer,
                    // or on 183 if WithEarlyMedia set) — safe point to start reading PCMReader/RTPReader
    OnState(func(state CallState))
    OnEnded(func(reason EndReason))
}
```

> **Callback contract:** All `On*` callbacks are fired in a dedicated goroutine per call.
> Callbacks **must return quickly**. Blocking stalls the call's event loop. Offload heavy work:
> ```go
> phone.OnIncoming(func(call xphone.Call) {
>     go func() {
>         call.Accept()
>     }()
> })
> ```

---

## State Machines

### PhoneState

```
Disconnected
    │
    ▼ Connect()
Registering
    │
    ▼ 200 OK
Registered ◄──────── re-REGISTER (keepalive)
    │
    ▼ Disconnect() / error
Unregistering
    │
    ▼ 200 OK
Disconnected
```

### Registration & Reconnect Behavior

- `Connect()` starts a **background registration loop** that runs until `Disconnect()` is called.
- On successful registration → `PhoneState = Registered`, `OnRegistered` fires.
- On transient failure (network drop, timeout, 5xx) → `PhoneState = Registering`, `OnUnregistered` fires, retry after `RegisterRetry`.
- `OnUnregistered` fires on **every loss of registration** including transient ones — treat as "temporarily unavailable", not fatal.
- If `RegisterMaxRetry` is exceeded → `PhoneState = RegistrationFailed`, `OnError` fires, loop stops.
- While reconnecting, **inbound calls are not possible** — the extension is unreachable on the PBX.
- Active calls at the time of transport failure end with `EndedByError` — mid-call SIP dialogs are not recovered.

### CallState

```
Idle
 │
 ├── (Inbound)  INVITE received ──► Ringing
 │                                      │
 │                                 Accept() ──► Active
 │                                 Reject() ──► Ended (EndedByRejected)
 │
 ├── (Outbound) Dial() ──► Dialing
 │                              │
 │                         180 Ringing ──► RemoteRinging
 │                         183 Session Progress ──► EarlyMedia
 │                                              │
 │                                         200 OK ──► Active
 │                                         End() / timeout ──► Ended (EndedByCancelled)
 │
 └── Active
         ├── Hold() ──► OnHold
         │                └── Resume() ──► Active
         │
         └── End() / BYE / error / MediaTimeout ──► Ended
```

---

## SIP Protocol Flows

### Registration (RFC 3261)

```
xphone                          PBX
  │── REGISTER ────────────────► │
  │◄─ 401 Unauthorized (nonce) ──│
  │── REGISTER (w/ auth) ───────► │
  │◄─ 200 OK ────────────────────│

  │  [every RegisterExpiry/2 seconds]
  │── REGISTER (refresh) ───────► │
  │◄─ 200 OK ────────────────────│

  │  [every NATKeepaliveInterval — UDP only]
  │── CRLF (keepalive) ─────────► │
```

### Inbound Call — Auto 100/180 behavior

```
PBX                             xphone
 │── INVITE ────────────────────► │
 │◄─ 100 Trying ─────────────────│  sent immediately and automatically
 │◄─ 180 Ringing ────────────────│  sent immediately after OnIncoming fires
 │   [app calls Accept()]
 │◄─ 200 OK (SDP answer) ────────│  call.State() == Active
 │── ACK ────────────────────────► │
 │   [RTP flows]
 │── BYE ────────────────────────► │  OnEnded(EndedByRemote)
 │◄─ 200 OK ─────────────────────│

 │   [app calls Reject(486, "Busy Here")]
 │◄─ 486 Busy Here ──────────────│  OnEnded(EndedByRejected)
 │── ACK ────────────────────────► │
```

> `100 Trying` is sent synchronously on INVITE receipt.
> `180 Ringing` is sent immediately after `OnIncoming` fires.
> The app only decides: `Accept()` or `Reject()`.

### Outbound Call

```
xphone                          PBX
 │── INVITE (SDP offer) ────────► │
 │◄─ 100 Trying ─────────────────│
 │◄─ 180 Ringing ────────────────│  call.State() == RemoteRinging
 │◄─ 200 OK (SDP answer) ────────│  call.State() == Active, Codec() available
 │── ACK ────────────────────────► │
 │   [RTP flows]
 │── BYE ────────────────────────► │  call.End()
 │◄─ 200 OK ─────────────────────│  OnEnded(EndedByLocal)
```

### Outbound Call — CANCEL

```
xphone                          PBX
 │── INVITE ────────────────────► │
 │◄─ 100 Trying ─────────────────│
 │◄─ 180 Ringing ────────────────│
 │   [app calls End()]
 │── CANCEL ────────────────────► │
 │◄─ 200 OK (CANCEL) ────────────│
 │◄─ 487 Request Terminated ─────│
 │── ACK ────────────────────────► │  OnEnded(EndedByCancelled)
```

### Early Media (183 Session Progress)

```
xphone                          PBX
 │── INVITE ────────────────────► │
 │◄─ 183 Session Progress (SDP) ─│  call.State() == EarlyMedia
 │   [RTP open only if WithEarlyMedia() set — otherwise discarded]
 │◄─ 200 OK ─────────────────────│  call.State() == Active
 │── ACK ────────────────────────► │
```

### Blind Transfer (REFER)

```
xphone          PBX (A)        Target (B)
 │── REFER ────► │
 │  (Refer-To: sip:B)
 │◄─ 202 Accepted
 │◄─ NOTIFY (100 Trying)
 │◄─ NOTIFY (200 OK)      ◄── INVITE ── │
 │── BYE ──────► │
 │◄─ 200 OK ────│
 OnEnded(EndedByTransfer)
```

### Hold / Resume (RFC 3264)

```
Hold:   re-INVITE with SDP a=sendonly (or a=inactive)
Resume: re-INVITE with SDP a=sendrecv
```

Incoming re-INVITEs from the PBX (hold, codec renegotiation, session refresh) are handled transparently. `OnHold` / `OnResume` fire accordingly. On codec change via re-INVITE, the media pipeline switches codec atomically and `Codec()` reflects the new value.

### Session Timers (RFC 4028)

`Session-Expires` and `Min-SE` headers are handled transparently. Session refresh re-INVITEs are sent automatically. Without this, many PBXs drop calls after ~30 minutes.

### DTMF (RFC 4733)

- Sent as RTP telephone-event packets (payload type 101 dynamic)
- Detected on inbound RTP stream
- Supported digits: `0-9`, `*`, `#`, `A-D`
- SIP INFO DTMF: deferred to v2

---

## Media Pipeline

### Architecture

```
PBX
 │  RTP/UDP
 ▼
[pion/rtp — packet parse]
 │
 ├──► RTPRawReader     (pre-jitter — wire-accurate, may be out of order)
 │
 ▼
[Jitter Buffer — default 50ms, configurable]
 │
 ├──► RTPReader        (post-jitter — reordered, deduplicated)
 │
 └──► Codec Decoder (PCMU/PCMA/G722/Opus)
          │  Opus resampled to PCMRate via bandlimited (sinc) resampler
          └──► PCMReader   ([]int16, mono, PCMRate Hz, PCMFrameSize samples)


Outbound (mutually exclusive):
 PCMWriter ──► Codec Encoder ──► pion/rtp ──► UDP ──► PBX   (default)
 RTPWriter ──────────────────► pion/rtp ──► UDP ──► PBX     (raw — PCMWriter ignored)
```

### RTP Clock Rates

| Codec | RTP Clock | Notes |
|---|---|---|
| PCMU | 8000 Hz | timestamp += 160 per 20ms frame |
| PCMA | 8000 Hz | timestamp += 160 per 20ms frame |
| G722 | 8000 Hz | clock is 8kHz despite 16kHz audio (RFC 3551) |
| Opus | 48000 Hz | timestamp += 960 per 20ms frame |

> Apps using `RTPWriter` must increment timestamps according to this table.
> The library does not validate or correct timestamps on outbound raw packets.

### PCM Format Contract

| Property | Value |
|---|---|
| Sample type | `int16` (signed 16-bit) |
| Channels | Mono (1) |
| Sample rate | `PCMRate` Hz (default: 8000) |
| Frame size | `PCMFrameSize` samples (default: 160 = 20ms @ 8kHz) |
| Resampler | Bandlimited (sinc) — Opus 48kHz → PCMRate only |

> Set `PCMRate: 48000` for native Opus quality. PCMU/PCMA/G722 are natively 8kHz — no resampling occurs unless `PCMRate` differs.

### PCMWriter Timing Contract

`PCMWriter` expects frames paced at **real-time rate** — one frame per frame duration (default: one 160-sample frame every 20ms).

- Frames arriving faster than real-time are queued up to the channel buffer, then **newest frames are dropped**
- Sending an audio burst all at once will saturate the buffer and cause audio artifacts
- Apps should pace writes using a ticker:

```go
ticker := time.NewTicker(20 * time.Millisecond)
for range ticker.C {
    call.PCMWriter() <- nextFrame()
}
```

### Channel Overflow Behavior

| Channel | Overflow policy |
|---|---|
| `RTPRawReader` | Drop **oldest** packet |
| `RTPReader` | Drop **oldest** packet |
| `PCMReader` | Drop **oldest** frame |
| `PCMWriter` | Drop **newest** frame (protect real-time pacing) |

All overflows are logged. Default buffer: 256 entries per channel.

### Jitter Buffer

- Default `50ms`, configurable via `JitterBuffer`
- Handles reordering and duplicate suppression
- `RTPRawReader` tapped **before** jitter buffer (wire-accurate)
- `RTPReader` and `PCMReader` fed **after** jitter buffer (clean stream)

### Media Timeout

- If no RTP is received for `MediaTimeout` (default `30s`) → BYE sent, `OnEnded(EndedByTimeout)`
- **Suspended automatically while call is `OnHold`** — some PBXs send no RTP during hold, which is valid behavior
- Timer resumes when call returns to `Active`

### RTP Port Management

- Each call gets a unique **even-numbered** port from `[RTPPortMin, RTPPortMax]`
- RTCP uses `RTP + 1` (odd), optional — many PBX environments run RTP-only
- Ports allocated on `Accept()` / `Dial()`, released on `Ended`
- After release, ports re-enter the pool and are reused via **round-robin allocation**
- If range is exhausted → `ErrNoRTPPortAvailable`
- Ports are never shared between concurrent calls

---

## Concurrency Model

- Each `Call` runs in its own goroutine pair: one for SIP events, one for RTP I/O
- `dialog.Map` (internal) is a `sync.Map` keyed by SIP dialog ID
- Callbacks fired in a **dedicated goroutine per call** — must return quickly (see above)
- `Phone.Dial()` blocks until `Active` or failure — timeout via `context`
- All channels buffered at 256. See overflow table above.

---

## Error Handling

```go
var (
    ErrNotRegistered      = errors.New("xphone: not registered")
    ErrCallNotFound       = errors.New("xphone: call not found")
    ErrInvalidState       = errors.New("xphone: invalid state for operation")
    ErrMediaTimeout       = errors.New("xphone: RTP media timeout")
    ErrDialTimeout        = errors.New("xphone: dial timeout exceeded before answer")
    ErrNoRTPPortAvailable = errors.New("xphone: RTP port range exhausted")
    ErrRegistrationFailed = errors.New("xphone: registration failed")
    ErrTransferFailed     = errors.New("xphone: transfer failed")
    ErrTLSConfigRequired  = errors.New("xphone: TLS transport requires TLSConfig")
)

type EndReason int
const (
    EndedByLocal      EndReason = iota // End() while Active/OnHold
    EndedByRemote                      // BYE received
    EndedByTimeout                     // MediaTimeout exceeded
    EndedByError                       // internal or transport error
    EndedByTransfer                    // REFER completed
    EndedByRejected                    // Reject() called
    EndedByCancelled                   // End() before 200 OK (outbound)
)
```

---

## Test Strategy

### Unit Tests

| Target | Approach |
|---|---|
| Registration state machine | Mock SIP transport, inject 200/401/503 responses |
| Reconnect loop | Simulate transport drop, verify retry + OnUnregistered cadence |
| Call state machine | Simulate INVITE/BYE/CANCEL/183 via mock dialog |
| End() semantics | Verify CANCEL pre-answer, BYE post-answer |
| DTMF encode/decode | Validate RFC 4733 packet construction and parsing |
| SDP offer/answer | Validate codec negotiation with fixture SDPs |
| Codec() after negotiation | Verify correct codec returned post-SDP exchange |
| re-INVITE codec change | Start PCMU, PBX sends re-INVITE switching to G722 — verify pipeline switches atomically |
| Hold/Resume | Verify SDP direction attributes in re-INVITE |
| Blind transfer | Mock REFER/NOTIFY, verify EndedByTransfer |
| Jitter buffer | Feed out-of-order packets, verify RTPReader reordered, RTPRawReader unmodified |
| RTP tap independence | Consume all three taps simultaneously, verify no crosstalk |
| Outbound mutex | Write to both RTPWriter and PCMWriter, verify PCMWriter dropped |
| RTPWriter responsibility | Feed packets with wrong timestamps, verify library sends as-is |
| PCMWriter pacing | Send burst of frames, verify oldest queued, newest dropped on overflow |
| Channel overflow | Saturate each channel, verify correct drop policy per channel |
| RTP port exhaustion | Fill port range, verify ErrNoRTPPortAvailable |
| Port round-robin | Release and re-allocate, verify round-robin ordering |
| Media timeout | Simulate RTP silence, verify OnEnded(EndedByTimeout) |
| Media timeout on hold | Simulate hold + RTP silence, verify timeout NOT triggered |
| Session timers | Inject Session-Expires header, verify refresh re-INVITE sent |
| Header copy safety | Mutate returned Headers() map, verify internal state unchanged |
| Opus resampling | Feed 48kHz Opus, verify PCMReader output matches PCMRate |
| LocalSDP / RemoteSDP | Verify SDP populated after SDP exchange, empty before |

### Integration Tests

- Spin up **Asterisk via Docker** in CI
- `sip.conf` registers extensions `1001`, `1002`, `1003`; `extensions.conf` routes between them
- Scenarios:
  - Register two instances, dial between them, verify media flows
  - Simulate registration expiry, verify auto re-registration
  - Simulate PBX drop (container pause), verify reconnect + OnUnregistered
  - Blind transfer: `1001` → `1002`, transfer to `1003`, verify EndedByTransfer
  - Early media: verify RTPReader opens on 183 with WithEarlyMedia, stays closed otherwise
  - re-INVITE codec change: verify pipeline switches mid-call without audio gap
- Docker Compose lives in `testutil/docker/`

### Testutil Package

```go
phone := testutil.NewMockPhone()
phone.SimulateIncoming("sip:1000@pbx.test")
assert.Equal(t, "accepted", phone.LastCall().State())
```

---

## Implementation Order

### Phase 1 — SIP Core
1. `registry.go` — REGISTER, auth, keepalive, reconnect loop
2. `transport.go` — UDP/TCP/TLS abstraction
3. `internal/dialog` — concurrent dialog map
4. `call.go` — inbound/outbound state machine, End() semantics

### Phase 2 — RTP Engine
1. Port allocator — round-robin, even/odd pairing
2. RTP session — pion/rtp integration
3. Jitter buffer — reorder, dedup
4. RTP taps — RTPRawReader, RTPReader

### Phase 3 — Codecs
1. PCMU / PCMA (G.711)
2. G.722
3. Opus + resampler

### Phase 4 — Features
1. Hold / Resume (re-INVITE)
2. Blind Transfer (REFER)
3. DTMF (RFC 4733)
4. Session timers (RFC 4028)

---

## Versioning & Stability Contract

- `v1.x.x` — `Phone` and `Call` interfaces are stable. No breaking changes.
- `internal/` is not public API.
- `testutil/` versioned separately.

---

## Out of Scope (v1)

- Video calls
- SRTP / media encryption
- WebRTC gateway
- Multi-PBX / failover registration
- Conference mixing
- Call recording (app-level via `RTPReader`)
- SIP INFO DTMF (deferred to v2)
- Attended transfer (deferred to v2)
