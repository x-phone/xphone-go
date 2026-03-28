# xphone

[![Go Reference](https://pkg.go.dev/badge/github.com/x-phone/xphone-go.svg)](https://pkg.go.dev/github.com/x-phone/xphone-go)
[![Go Report Card](https://goreportcard.com/badge/github.com/x-phone/xphone-go)](https://goreportcard.com/report/github.com/x-phone/xphone-go)
[![CI](https://github.com/x-phone/xphone-go/actions/workflows/ci.yml/badge.svg)](https://github.com/x-phone/xphone-go/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/github/go-mod-go-version/x-phone/xphone-go)](https://github.com/x-phone/xphone-go)

A Go library for SIP calling and RTP media. Register with a SIP trunk or PBX, or accept calls directly as a SIP server — and get decoded PCM audio frames through Go channels.

> Also available in [Rust](https://github.com/x-phone/xphone-rust).

## Table of Contents

- [Status](#status--beta) | [Scope and Limitations](#scope-and-limitations) | [Tested Against](#tested-against) | [Use Cases](#use-cases)
- [Quick Start](#quick-start) | [Connection Modes](#connection-modes) | [Working with Audio](#working-with-audio)
- [Features](#features) | [Call States](#call-states) | [Call Control](#call-control) | [Media Pipeline](#media-pipeline)
- [Configuration](#configuration) | [RTP Port Range](#rtp-port-range) | [NAT Traversal](#nat-traversal) | [Opus Codec](#opus-codec)
- [Testing](#testing) | [Example App](#example-app) | [Logging](#logging) | [Stack](#stack) | [Roadmap](#roadmap)

---

## Status — Beta

xphone is in active development and used in internal production workloads. APIs may change between minor versions. If you're evaluating, start with the [Quick Start](#quick-start) below or the [demos repo](https://github.com/x-phone/demos).

---

## Scope and limitations

xphone is a **voice data-plane library** — SIP signaling and RTP media. It is not a telephony platform.

**You are responsible for:**

- Billing, number provisioning, and call routing rules
- Recording storage and playback infrastructure
- High availability, persistence, and failover
- Rate limiting, authentication, and abuse prevention at the application level

**Security boundaries:**

- SRTP uses SDES key exchange only. DTLS-SRTP is not supported — xphone cannot interop with WebRTC endpoints that require it.
- TLS is supported for SIP transport. See [Configuration](#configuration) for transport options.
- There is no built-in authentication layer for your application — xphone authenticates to SIP servers, not your end users.

**Codec constraints:**

- Opus and G.729 require CGO and external C libraries. The default build is CGO-free (G.711, G.722 only).
- PCM sample rate is fixed at 8 kHz (narrowband) or 16 kHz (G.722 wideband). There is no configurable sample rate.

---

## Tested against

| Category | Tested with |
|---|---|
| **SIP trunks** | Telnyx, Twilio SIP, VoIP.ms, Vonage |
| **PBXes** | Asterisk, FreeSWITCH, 3CX |
| **Integration tests** | [fakepbx](https://github.com/x-phone/fakepbx) (in-process, no Docker) + [xpbx](https://github.com/x-phone/xpbx) (Dockerized Asterisk) in CI |
| **Unit tests** | MockPhone & MockCall — full Phone/Call interface mocks |

This is not a comprehensive compatibility matrix. If you hit issues with a provider or PBX not listed here, please open an issue.

---

## Use cases

- **AI voice agents** — pipe call audio directly into your STT/LLM/TTS pipeline without a telephony platform
- **Softphones and click-to-call** — embed SIP calling into any Go application against a trunk or PBX
- **Call recording and monitoring** — tap the PCM audio stream for transcription, analysis, or storage
- **Outbound dialers** — programmatic dialing with DTMF detection for IVR automation
- **Unit-testable call flows** — MockPhone and MockCall let you test every call branch without a SIP server

---

## Quick Start

### Install

```bash
go get github.com/x-phone/xphone-go
```

Requires Go 1.23+.

### Receive calls

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
        xphone.WithCredentials("1001", "secret", "sip.telnyx.com"),
        xphone.WithRTPPorts(10000, 20000),
    )

    phone.OnRegistered(func() {
        fmt.Println("Registered -- ready to receive calls")
    })

    phone.OnIncoming(func(call xphone.Call) {
        fmt.Printf("Incoming call from %s\n", call.From())
        call.Accept()

        go func() {
            for frame := range call.PCMReader() {
                // frame is []int16, mono, 8000 Hz, 160 samples (20ms)
                transcribe(frame)
            }
        }()
    })

    if err := phone.Connect(context.Background()); err != nil {
        log.Fatal(err)
    }

    select {}
}
```

PCM format: `[]int16`, mono, 8000 Hz, 160 samples per frame (20ms) — the standard input format for most speech-to-text APIs.

### Make an outbound call

```go
call, err := phone.Dial(ctx, "+15551234567",
    xphone.WithEarlyMedia(),
    xphone.WithDialTimeout(30 * time.Second),
)
if err != nil {
    log.Fatal(err)
}

go func() {
    for frame := range call.PCMReader() {
        processAudio(frame)
    }
}()
```

`Dial` accepts a full SIP URI (`"sip:1002@pbx.example.com"`) or just the user part (`"1002"`), in which case your configured SIP server is used.

---

## Connection Modes

xphone supports two ways to connect to the SIP world. Both produce the same `Call` interface — accept, end, DTMF, PCMReader/Writer are identical.

### Phone mode (SIP client)

Registers with a SIP server like a normal endpoint. Use this with SIP trunks (Telnyx, Vonage), PBXes (Asterisk, FreeSWITCH), or any SIP registrar. No PBX is required — you can register directly with a SIP trunk provider:

```go
phone := xphone.New(
    xphone.WithCredentials("1001", "secret", "sip.telnyx.com"),
)
phone.OnIncoming(func(call xphone.Call) { call.Accept() })
phone.Connect(ctx)
```

### Server mode (SIP trunk)

Accepts and places calls directly with trusted SIP peers — no registration required. Use this when trunk providers send INVITEs to your public IP, or when a PBX routes calls to your application:

```go
server := xphone.NewServer(xphone.ServerConfig{
    Listen:     "0.0.0.0:5080",
    RTPPortMin: 10000,
    RTPPortMax: 20000,
    RTPAddress: "203.0.113.1",  // your public IP
    Peers: []xphone.PeerConfig{
        {Name: "twilio", Hosts: []string{"54.172.60.0/30", "54.244.51.0/30"}},
        {Name: "office-pbx", Host: "192.168.1.10"},
    },
})
server.OnIncoming(func(call xphone.Call) { call.Accept() })
server.Listen(ctx)
```

Peers are authenticated by IP/CIDR or SIP digest auth. Per-peer codec and RTP address overrides are supported.

> **Which mode?** Use **Phone** when you register to a SIP server (most setups). Use **Server** when SIP peers send INVITEs directly to your application (Twilio SIP Trunk, direct PBX routing, peer-to-peer).

---

## Working with Audio

xphone exposes audio as a stream of PCM frames through Go channels.

### Frame format

| Property | Value |
|---|---|
| Encoding | 16-bit signed PCM |
| Channels | Mono |
| Sample rate | 8000 Hz |
| Samples per frame | 160 |
| Frame duration | 20ms |

### Reading inbound audio

`call.PCMReader()` returns a `<-chan []int16`. Each receive gives you one 20ms frame of decoded audio from the remote party:

```go
go func() {
    for frame := range call.PCMReader() {
        sendToSTT(frame)
    }
    // channel closes when the call ends
}()
```

> **Important:** Read frames promptly. The inbound buffer holds 256 frames (~5 seconds). If you fall behind, the oldest frames are silently dropped.

### Writing outbound audio

`call.PCMWriter()` returns a `chan<- []int16`. Send one 20ms frame at a time:

```go
go func() {
    ticker := time.NewTicker(20 * time.Millisecond)
    defer ticker.Stop()
    for range ticker.C {
        frame := getNextTTSFrame() // []int16, 160 samples
        select {
        case call.PCMWriter() <- frame:
        default:
            // outbound buffer full -- frame dropped, keep going
        }
    }
}()
```

> **Important:** `PCMWriter()` sends each buffer as an RTP packet immediately — the caller must provide frames at real-time rate (one 160-sample frame every 20ms). For TTS or file playback, use `PacedPCMWriter()` instead.

### Paced writer (for TTS / pre-generated audio)

`call.PacedPCMWriter()` accepts arbitrary-length PCM buffers and handles framing + pacing internally:

```go
ttsAudio := elevenLabs.Synthesize("Hello, how can I help you?")
call.PacedPCMWriter() <- ttsAudio
```

### Raw RTP access

For lower-level control — pre-encoded audio, custom codecs, or RTP header inspection:

```go
go func() {
    for pkt := range call.RTPReader() {
        processRTP(pkt) // *rtp.Packet (pion/rtp)
    }
}()

call.RTPWriter() <- myRTPPacket
```

> `RTPWriter` and `PCMWriter` are mutually exclusive — if you write to `RTPWriter`, `PCMWriter` is ignored for that call.

### Converting to float32

```go
func pcmToFloat32(frame []int16) []float32 {
    out := make([]float32, len(frame))
    for i, s := range frame {
        out[i] = float32(s) / 32768.0
    }
    return out
}
```

---

## Features

### Calling — stable

- SIP registration with auto-reconnect and keepalive
- Inbound and outbound calls
- Hold / resume (re-INVITE)
- Blind transfer (REFER) and attended transfer (REFER with Replaces, RFC 3891)
- Call waiting (`Phone.Calls()` API)
- Session timers (RFC 4028)
- Mute / unmute
- 302 redirect following
- Early media (183 Session Progress)
- `ReplaceAudioWriter` — atomic audio source swap (e.g., music on hold)
- Outbound proxy routing (`WithOutboundProxy`)
- Separate outbound credentials (`WithOutboundCredentials`)
- Custom headers on outbound INVITEs (`WithHeader`, `DialOptions.CustomHeaders`)
- `Server.DialURI` — dial arbitrary SIP URIs without pre-configured peers
- Transfer failure surfaced via `EndedByTransferFailed` end reason

### DTMF — stable

- RFC 4733 (RTP telephone-events)
- SIP INFO (RFC 2976)

### Audio codecs — stable

- G.711 u-law (PCMU), G.711 A-law (PCMA) — built-in
- G.722 wideband — built-in
- Opus — optional, requires CGO + libopus (`-tags opus`)
- G.729 — optional, requires CGO + bcg729 (`-tags g729`)
- Jitter buffer

### Video — newer, less production mileage

- H.264 (RFC 6184) and VP8 (RFC 7741)
- Depacketizer/packetizer pipeline
- Mid-call video upgrade/downgrade (re-INVITE)
- VideoReader / VideoWriter / VideoRTPReader / VideoRTPWriter
- RTCP PLI/FIR for keyframe requests

### Security — stable

- SRTP (AES_CM_128_HMAC_SHA1_80) with SDES key exchange
- SRTP replay protection (RFC 3711)
- SRTCP encryption (RFC 3711 §3.4)
- Key material zeroization
- Separate SRTP contexts for audio and video

### Network — stable

- TCP and TLS SIP transport
- STUN NAT traversal (RFC 5389)
- TURN relay for symmetric NAT (RFC 5766)
- ICE-Lite (RFC 8445 §2.2)
- RTCP Sender/Receiver Reports (RFC 3550)

### Messaging — newer, less production mileage

- SIP MESSAGE (RFC 3428)
- SIP SUBSCRIBE/NOTIFY (RFC 6665)
- MWI / voicemail notification (RFC 3842)
- BLF / Busy Lamp Field monitoring
- SIP Presence (RFC 3856)

### Testing — stable

- MockPhone and MockCall — full interface mocks for unit testing

---

## Call States

```
Idle -> Ringing (inbound) or Dialing (outbound)
     -> RemoteRinging -> Active <-> OnHold -> Ended
```

```go
call.OnState(func(state xphone.CallState) {
    fmt.Printf("State: %v\n", state)
})

call.OnEnded(func(reason xphone.EndReason) {
    fmt.Printf("Ended: %v\n", reason)
})
```

---

## Call Control

```go
call.Hold()
call.Resume()

call.BlindTransfer("sip:1003@pbx.example.com")
callA.AttendedTransfer(callB)

call.Mute()
call.Unmute()

call.SendDTMF("5")
call.OnDTMF(func(digit string) {
    fmt.Printf("Received: %s\n", digit)
})

// Mid-call video upgrade
call.AddVideo(xphone.VideoCodecH264, xphone.VideoCodecVP8)
call.OnVideoRequest(func(req *xphone.VideoUpgradeRequest) {
    req.Accept()
})
call.OnVideo(func() {
    // read frames from call.VideoReader()
})

phone.SendMessage(ctx, "sip:1002@pbx", "Hello!")
```

---

## Media Pipeline

### Audio

```
Inbound:
  SIP Trunk -> RTP/UDP -> RTPRawReader (pre-jitter)
                        -> Jitter Buffer -> RTPReader (post-jitter)
                                          -> Codec Decode -> PCMReader ([]int16)

Outbound (mutually exclusive):
  PCMWriter      -> Codec Encode -> RTP/UDP -> SIP Trunk   (caller paces at 20ms)
  PacedPCMWriter -> Auto-frame + 20ms ticker -> Codec Encode -> RTP/UDP -> SIP Trunk
  RTPWriter      -> RTP/UDP -> SIP Trunk   (raw mode — PCMWriter ignored)
```

### Video

```
Inbound:
  SIP Trunk -> RTP/UDP -> Depacketizer (H.264/VP8) -> VideoReader (NAL units / frames)
                        -> VideoRTPReader (raw video RTP packets)

Outbound (mutually exclusive):
  VideoWriter -> Packetizer (H.264/VP8) -> RTP/UDP -> SIP Trunk
  VideoRTPWriter -> RTP/UDP -> SIP Trunk   (raw mode)
```

Video uses a separate RTP port and independent SRTP contexts. RTCP PLI/FIR requests trigger keyframe generation on the sender side.

All channels are buffered (256 entries). Inbound taps drop oldest on overflow; outbound writers drop newest. Audio frames are 160 samples at 8000 Hz = 20ms. Video frames carry codec-specific NAL units (H.264) or encoded frames (VP8).

---

## Configuration

```go
phone := xphone.New(
    xphone.WithCredentials("1001", "secret", "pbx.example.com"),
    xphone.WithTransport("udp", nil),                      // "udp" | "tcp" | "tls"
    xphone.WithRTPPorts(10000, 20000),                      // RTP port range
    xphone.WithCodecs(xphone.CodecOpus, xphone.CodecPCMU),  // codec preference
    xphone.WithJitterBuffer(50 * time.Millisecond),
    xphone.WithMediaTimeout(30 * time.Second),
    xphone.WithNATKeepalive(25 * time.Second),
    xphone.WithStunServer("stun.l.google.com:19302"),
    xphone.WithSRTP(),
    xphone.WithDtmfMode(xphone.DtmfModeRFC4733),           // or DtmfModeSIPInfo
    xphone.WithICE(true),                                   // ICE-Lite
    xphone.WithTurnServer("turn.example.com:3478"),
    xphone.WithTurnCredentials("user", "pass"),
    xphone.WithOutboundProxy("sip:proxy.example.com:5060"),   // route INVITEs via proxy
    xphone.WithOutboundCredentials("trunk-user", "trunk-pass"), // separate INVITE auth
    xphone.WithLogger(slog.Default()),
)
```

See [pkg.go.dev](https://pkg.go.dev/github.com/x-phone/xphone-go) for all options.

---

## RTP Port Range

Each active call requires an even-numbered UDP port for RTP audio. Configure an explicit range for production deployments behind firewalls:

```go
phone := xphone.New(
    xphone.WithCredentials("1001", "secret", "sip.telnyx.com"),
    xphone.WithRTPPorts(10000, 20000),
)
```

Only even ports are used (per RTP spec). Maximum concurrent audio-only calls = `(max - min) / 2`.

| Range | Even ports | Max concurrent calls |
|---|---|---|
| 10000–10100 | 50 | ~50 |
| 10000–12000 | 1000 | ~1000 |
| 10000–20000 | 5000 | ~5000 |

**When ports run out:** inbound calls receive a `500 Internal Server Error` and outbound dials fail with an error. Widen the range before investigating SIP server configuration.

If `WithRTPPorts` is not called, the OS assigns ephemeral ports. This works for development but is impractical in production where firewall rules need a known range.

---

## NAT Traversal

### STUN (most deployments)

Discovers your public IP via a STUN Binding Request:

```go
phone := xphone.New(
    xphone.WithCredentials("1001", "secret", "sip.telnyx.com"),
    xphone.WithStunServer("stun.l.google.com:19302"),
)
```

### TURN (symmetric NAT)

For environments where STUN alone fails (cloud VMs, corporate firewalls):

```go
phone := xphone.New(
    xphone.WithCredentials("1001", "secret", "sip.telnyx.com"),
    xphone.WithTurnServer("turn.example.com:3478"),
    xphone.WithTurnCredentials("user", "pass"),
)
```

### ICE-Lite

SDP-level candidate negotiation (RFC 8445 §2.2):

```go
phone := xphone.New(
    xphone.WithCredentials("1001", "secret", "sip.telnyx.com"),
    xphone.WithICE(true),
    xphone.WithStunServer("stun.l.google.com:19302"),
)
```

> Only enable STUN/TURN/ICE when the SIP server is on the public internet. Do not enable it when connecting via VPN or private network.

---

## Opus Codec

Opus is optional and requires CGO + libopus. The default build is CGO-free.

### Install libopus

```bash
# Debian / Ubuntu
sudo apt-get install libopus-dev libopusfile-dev

# macOS
brew install opus opusfile
```

### Build with Opus

```bash
go build -tags opus ./...
go test -tags opus ./...
```

### Usage

```go
phone := xphone.New(
    xphone.WithCredentials("1001", "secret", "sip.telnyx.com"),
    xphone.WithCodecs(xphone.CodecOpus, xphone.CodecPCMU),
)
```

Opus runs at 8kHz natively — no resampling needed. PCM frames remain `[]int16`, mono, 160 samples (20ms). RTP timestamps use 48kHz clock per RFC 7587.

Without the `opus` build tag, `CodecOpus` is accepted in configuration but will not be negotiated.

---

## Testing

### Unit tests with mocks

`MockPhone` and `MockCall` implement the `Phone` and `Call` interfaces:

```go
phone := xphone.NewMockPhone()
phone.Connect(context.Background())

phone.OnIncoming(func(c xphone.Call) {
    c.Accept()
})
phone.SimulateIncoming("sip:1001@pbx")

assert.Equal(t, xphone.StateActive, phone.LastCall().State())
```

```go
call := xphone.NewMockCall()
call.Accept()
call.SendDTMF("5")
assert.Equal(t, []string{"5"}, call.SentDTMF())

call.SimulateDTMF("9")
call.InjectRTP(pkt)
```

### Integration tests with FakePBX (no Docker)

```bash
go test -v -run TestFakePBX ./...
go test -v -run TestServerFakePBX ./...
```

### End-to-end tests with Asterisk

```bash
cd testutil/docker && docker compose up -d
go test -tags=integration -v -count=1 ./...
cd testutil/docker && docker compose down
```

---

## Example App

`examples/sipcli` is a terminal SIP client with registration, calls, hold, resume, DTMF, mute, transfer, echo mode, and speaker output:

```bash
cd examples/sipcli
go run . -profile myserver       # from ~/.sipcli.yaml
go run . -server pbx.example.com -user 1001 -pass secret
```

---

## Logging

xphone uses Go's `log/slog`:

```go
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

If no logger is provided, `slog.Default()` is used.

---

## Stack

| Layer | Implementation |
|---|---|
| SIP Signaling | [sipgo](https://github.com/emiago/sipgo) |
| RTP / SRTP | [pion/rtp](https://github.com/pion/rtp) + built-in SRTP (AES_CM_128_HMAC_SHA1_80) |
| G.711 / G.722 | Built-in (PCMU, PCMA) + [gotranspile/g722](https://github.com/gotranspile/g722) |
| G.729 | [AoiEnoki/bcg729](https://github.com/AoiEnoki/bcg729) (optional, `-tags g729`) |
| Opus | [hraban/opus](https://github.com/hraban/opus) (optional, `-tags opus`) |
| H.264 / VP8 | Built-in packetizer/depacketizer (RFC 6184, RFC 7741) |
| RTCP | Built-in (RFC 3550 SR/RR + PLI/FIR) |
| Jitter Buffer | Built-in |
| STUN | Built-in (RFC 5389) |
| TURN | Built-in (RFC 5766) |
| ICE-Lite | Built-in (RFC 8445 §2.2) |
| TUI (sipcli) | [bubbletea](https://github.com/charmbracelet/bubbletea) + [lipgloss](https://github.com/charmbracelet/lipgloss) |

---

## Roadmap

- DTLS-SRTP key exchange (WebRTC interop)
- Full ICE (RFC 5245)

---

## License

MIT
