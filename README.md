# xphone

[![Go Reference](https://pkg.go.dev/badge/github.com/x-phone/xphone-go.svg)](https://pkg.go.dev/github.com/x-phone/xphone-go)
[![Go Report Card](https://goreportcard.com/badge/github.com/x-phone/xphone-go)](https://goreportcard.com/report/github.com/x-phone/xphone-go)
[![CI](https://github.com/x-phone/xphone-go/actions/workflows/ci.yml/badge.svg)](https://github.com/x-phone/xphone-go/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/github/go-mod/go-version/x-phone/xphone-go)](https://github.com/x-phone/xphone-go)

A Go library for embedding SIP telephony into any application. xphone wraps a SIP user agent and exposes an event-driven API for managing concurrent VoIP calls — handling SIP signaling, registration, RTP media, codecs, and call control so you don't have to.

> **xphone** is also available in [Rust](https://github.com/x-phone/xphone-rust).

## Features

- **SIP registration** with auth, keepalive, and automatic reconnect
- **Inbound & outbound calls** with full state machine (ringing, active, hold, transfer)
- **RTP media pipeline** with jitter buffer, codec encode/decode, and DTMF
- **Codecs**: G.711 µ-law (PCMU), G.711 A-law (PCMA), G.722
- **DTMF** send/receive (RFC 4733)
- **Hold/Resume** via re-INVITE
- **Blind transfer** via REFER
- **Session timers** (RFC 4028) — automatic refresh to prevent PBX timeouts
- **Mute/Unmute** — suppress outbound audio without affecting inbound
- **PCM and RTP access** — decoded audio frames or raw RTP packets, your choice
- **Concurrency-safe** — each call runs in its own goroutine, all state is protected
- **Testable** — `MockPhone` and `MockCall` included for unit testing

## Install

```bash
go get github.com/x-phone/xphone-go
```

Requires Go 1.23+.

## Example App

`examples/sipcli` is a fully interactive terminal SIP client that exercises the library's event-driven API — registration, inbound/outbound calls, hold, resume, DTMF, mute, and transfer:

```bash
cd examples/sipcli
go run . -server pbx.example.com -user 1001 -pass secret
```

## Quick Start

### Register and receive calls

```go
package main

import (
    "context"
    "fmt"
    "log"

    xphone "github.com/x-phone/xphone-go"
)

func main() {
    phone := xphone.New(
        xphone.WithCredentials("1001", "secret", "pbx.example.com"),
    )

    phone.OnIncoming(func(call xphone.Call) {
        fmt.Printf("Incoming call from %s\n", call.From())
        call.Accept()
    })

    phone.OnRegistered(func() {
        fmt.Println("Registered with PBX")
    })

    if err := phone.Connect(context.Background()); err != nil {
        log.Fatal(err)
    }

    select {} // block forever
}
```

### Make an outbound call

`Dial` accepts a full SIP URI (`"sip:1002@pbx.example.com"`) or just the user part (`"1002"`), in which case the configured host is used.

```go
call, err := phone.Dial(ctx, "sip:1002@pbx.example.com",
    xphone.WithCallerID("Support"),
    xphone.WithDialTimeout(20 * time.Second),
)
if err != nil {
    log.Fatal(err)
}

// Read decoded audio
go func() {
    for frame := range call.PCMReader() {
        // frame is []int16, mono, 8000 Hz, 160 samples (20ms)
        processAudio(frame)
    }
}()

// Send audio
ticker := time.NewTicker(20 * time.Millisecond)
for range ticker.C {
    call.PCMWriter() <- nextFrame()
}
```

### Send DTMF

```go
call.SendDTMF("5")
call.OnDTMF(func(digit string) {
    fmt.Printf("Received DTMF: %s\n", digit)
})
```

### Hold, resume, transfer

```go
call.Hold()
call.Resume()
call.BlindTransfer("sip:1003@pbx.example.com")
```

### Mute/unmute

```go
call.Mute()   // suppresses outbound audio, inbound still flows
call.Unmute()
```

## Configuration

```go
phone := xphone.New(
    xphone.WithCredentials("1001", "secret", "pbx.example.com"),
    xphone.WithTransport("udp", nil),                     // "udp" | "tcp" | "tls"
    xphone.WithRTPPorts(10000, 20000),                     // RTP port range
    xphone.WithCodecs(xphone.CodecPCMU, xphone.CodecG722), // codec preference
    xphone.WithJitterBuffer(50 * time.Millisecond),
    xphone.WithMediaTimeout(30 * time.Second),
    xphone.WithNATKeepalive(25 * time.Second),
    xphone.WithLogger(slog.Default()),
)
```

See the [Go documentation](https://pkg.go.dev/github.com/x-phone/xphone-go) for all options.

## Logging

xphone uses Go's standard `log/slog`. Pass your own logger to control level, format, and destination:

```go
// Structured JSON logs at debug level
phone := xphone.New(
    xphone.WithLogger(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
        Level: slog.LevelDebug,
    }))),
)

// Silence all library logs
phone := xphone.New(
    xphone.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
)
```

If no logger is provided, `slog.Default()` is used. The library logs registration events, call state transitions, media timeouts, and errors.

## Call States

```
Idle → Ringing (inbound) or Dialing (outbound)
  → RemoteRinging → Active ⇄ OnHold → Ended
```

Track state changes:

```go
call.OnState(func(state xphone.CallState) {
    fmt.Printf("Call state: %v\n", state)
})

call.OnEnded(func(reason xphone.EndReason) {
    fmt.Printf("Call ended: %v\n", reason)
})
```

## Media Pipeline

```
Inbound:
  PBX → RTP/UDP → RTPRawReader (pre-jitter)
                 → Jitter Buffer → RTPReader (post-jitter)
                                  → Codec Decode → PCMReader ([]int16, 8kHz)

Outbound (mutually exclusive):
  PCMWriter → Codec Encode → RTP/UDP → PBX
  RTPWriter → RTP/UDP → PBX   (raw mode — PCMWriter ignored)
```

PCM format: `[]int16`, mono, 8000 Hz, 160 samples per frame (20ms).

All channels are buffered (256 entries). Inbound taps drop oldest on overflow; PCMWriter drops newest.

## Testing

`MockPhone` and `MockCall` implement the `Phone` and `Call` interfaces for unit testing:

```go
phone := xphone.NewMockPhone()
phone.Connect(context.Background())

// Simulate an incoming call
phone.OnIncoming(func(c xphone.Call) {
    c.Accept()
})
phone.SimulateIncoming("sip:1001@pbx")

// Verify
assert.Equal(t, xphone.StateActive, phone.LastCall().State())
```

```go
call := xphone.NewMockCall()
call.Accept()
call.SendDTMF("5")
assert.Equal(t, []string{"5"}, call.SentDTMF())

// Simulate inbound events
call.SimulateDTMF("9")
call.InjectRTP(pkt)
```

## Integration Tests

**[FakePBX](https://github.com/x-phone/fakepbx) (no Docker required)** — fast, in-process SIP tests that cover registration, dial, BYE, hold, DTMF, RTP, busy, and provisionals. These run with the standard test suite:

```bash
go test -v -run TestFakePBX ./...
```

**Asterisk via Docker** — full end-to-end tests against a real PBX:

```bash
cd testutil/docker && docker compose up -d
go test -tags=integration -v -count=1 ./...
cd testutil/docker && docker compose down
```

## Not Yet Implemented

The following are planned for a future release:

- **Opus codec** — `pion/opus` is currently decode-only (no encoder)
- **Attended transfer**
- **SIP INFO DTMF**
- **`Config.PCMRate`** — configurable PCM sample rate (currently fixed at 8000 Hz)

## Stack

- SIP Signaling: [sipgo](https://github.com/emiago/sipgo)
- RTP Media: [pion/rtp](https://github.com/pion/rtp)
- Codecs: [g722](https://github.com/gotranspile/g722) (G.722), built-in (G.711)

## License

MIT
