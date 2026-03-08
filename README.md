# xphone

[![Go Reference](https://pkg.go.dev/badge/github.com/x-phone/xphone-go.svg)](https://pkg.go.dev/github.com/x-phone/xphone-go)
[![Go Report Card](https://goreportcard.com/badge/github.com/x-phone/xphone-go)](https://goreportcard.com/report/github.com/x-phone/xphone-go)
[![CI](https://github.com/x-phone/xphone-go/actions/workflows/ci.yml/badge.svg)](https://github.com/x-phone/xphone-go/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/github/go-mod/go-version/x-phone/xphone-go)](https://github.com/x-phone/xphone-go)

**A Go library for embedding real phone calls into any application.**
No PBX. No Twilio. No per-minute fees. Just clean PCM audio, in and out.

xphone handles SIP signaling, RTP media, codecs, and call state so you can focus on what your application actually does with the audio — whether that's feeding frames to a speech model, recording to disk, or building a full softphone.

> **xphone** is also available in [Rust](https://github.com/x-phone/xphone-rust).

---

## Why xphone?

Building anything that needs to make or receive real phone calls is surprisingly painful. Your options are usually:

- **Twilio / Vonage / Telnyx SDKs** — easy to start, but you're paying platform fees per minute, your audio routes through their cloud, and the media pipeline is a black box.
- **Raw SIP libraries** — full control, but you wire everything yourself: signaling, RTP sessions, jitter buffers, codec negotiation, call state machines. Weeks of work before you can answer a call.
- **Asterisk / FreeSWITCH via AMI/ARI** — mature and powerful, but now you're running and operating a PBX just to make a call from your application.

xphone sits in the middle: a high-level, event-driven Go API that handles all the protocol complexity and hands you clean PCM audio frames — ready to pipe into Whisper, Deepgram, or any audio pipeline you choose. Your audio never leaves your infrastructure unless you choose to send it somewhere.

---

## What can you build with it?

### AI Voice Agents
Connect a real phone number directly to your LLM pipeline. No cloud telephony platform required.

```
DID (phone number)
    +-- SIP Trunk (Telnyx, Twilio SIP, Vonage...)
            +-- xphone
                    |-- PCMReader -> Whisper / Deepgram (speech-to-text)
                    +-- PCMWriter <- ElevenLabs / TTS (text-to-speech)
```

Your bot gets a real phone number, registers directly with a SIP trunk provider, and handles calls end-to-end — no Asterisk, no middleman, no per-minute platform fees.

### Softphones & Click-to-Call
Embed a SIP phone into any Go application. Accept calls, dial out, hold, transfer — all from code. Works against any SIP PBX (Asterisk, FreeSWITCH, 3CX, Cisco) or directly to a SIP trunk.

### Call Recording & Monitoring
Tap into the PCM audio stream on any call and write it to disk, stream it to S3, or run real-time transcription and analysis.

### Outbound Dialers
Programmatically dial numbers, play audio, detect DTMF responses — classic IVR automation without the IVR infrastructure.

### Unit-testable Call Flows
`MockPhone` and `MockCall` implement the full `Phone` and `Call` interfaces. Test every branch of your call logic — accept, reject, hold, transfer, DTMF, hangup — without a real SIP server or network. This is a first-class design goal, not an afterthought.

---

## No PBX required

A common misconception: you don't need Asterisk or FreeSWITCH to use xphone. A SIP trunk is just a SIP server — xphone registers with it directly, exactly like a desk phone would.

```go
phone := xphone.New(
    xphone.WithCredentials("your-username", "your-password", "sip.telnyx.com"),
)
```

That's it. Your application registers with the SIP trunk, receives calls on your DID, and can dial out — no additional infrastructure.

> A PBX only becomes relevant when you need to route calls across multiple agents or extensions. For single-purpose applications — a voice bot, a recorder, a dialer — xphone + SIP trunk is all you need.

---

## Self-hosted vs cloud telephony

Most cloud telephony SDKs are excellent for getting started, but come with tradeoffs that matter at scale or in regulated environments:

| | xphone + SIP Trunk | Cloud Telephony SDK |
|---|---|---|
| **Cost** | SIP trunk rates only | Per-minute platform fees on top |
| **Audio privacy** | Media stays on your infrastructure | Audio routed through provider cloud |
| **Latency** | Direct RTP to your server | Extra hop through provider media servers |
| **Control** | Full access to raw PCM / RTP | API-level access only |
| **Compliance** | You control data residency | Provider's data policies apply |
| **Complexity** | You manage the SIP stack | Provider handles it |

xphone is the right choice when cost, latency, privacy, or compliance make self-hosting the media pipeline worth it.

> **SIP trunk providers** (Telnyx, Twilio SIP, Vonage, Bandwidth, and many others) offer DIDs and SIP credentials at wholesale rates — typically $0.001-$0.005/min, with no additional platform markup when you bring your own SIP client.

---

## Quick Start

### Install

```bash
go get github.com/x-phone/xphone-go
```

Requires Go 1.23+.

---

### Build an AI voice agent in ~30 lines

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

        // Read decoded audio -- pipe to Whisper, Deepgram, etc.
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

    select {} // block forever
}
```

PCM format: `[]int16`, mono, 8000 Hz, 160 samples per frame (20ms) — the standard input format for most speech-to-text APIs.

---

### Make an outbound call

```go
call, err := phone.Dial(ctx, "+15551234567",
    xphone.WithEarlyMedia(),                    // hear ringback tones before answer
    xphone.WithDialTimeout(30 * time.Second),
)
if err != nil {
    log.Fatal(err)
}

// Stream audio in and out.
go func() {
    for frame := range call.PCMReader() {
        processAudio(frame)
    }
}()
```

`Dial` accepts a full SIP URI (`"sip:1002@pbx.example.com"`) or just the user part (`"1002"`), in which case your configured SIP server is used.

---

## Features

| Feature | Status |
|---|---|
| SIP Registration (auth, keepalive, auto-reconnect) | Done |
| Inbound & outbound calls | Done |
| Hold / Resume (re-INVITE) | Done |
| Blind transfer (REFER) | Done |
| DTMF send/receive (RFC 4733) | Done |
| Session timers (RFC 4028) | Done |
| Mute / Unmute | Done |
| G.711 u-law (PCMU), G.711 A-law (PCMA) | Done |
| G.722 wideband codec | Done |
| Opus codec (optional, requires libopus) | Done |
| PCM audio frames (`[]int16`) and raw RTP access | Done |
| Jitter buffer | Done |
| SRTP (encrypted media, AES_CM_128_HMAC_SHA1_80) | Done |
| SRTP replay protection (RFC 3711) | Done |
| RTCP Sender/Receiver Reports (RFC 3550) | Done |
| 302 redirect following | Done |
| TCP and TLS SIP transport | Done |
| Early media (183 Session Progress) | Done |
| STUN NAT traversal (RFC 5389) | Done |
| MockPhone & MockCall for unit testing | Done |
| Attended transfer | Planned |

---

## Configuration

```go
phone := xphone.New(
    xphone.WithCredentials("1001", "secret", "pbx.example.com"),
    xphone.WithTransport("udp", nil),                      // "udp" | "tcp" | "tls"
    xphone.WithRTPPorts(10000, 20000),                      // RTP port range
    xphone.WithCodecs(xphone.CodecOpus, xphone.CodecPCMU),  // codec preference (Opus requires -tags opus)
    xphone.WithJitterBuffer(50 * time.Millisecond),
    xphone.WithMediaTimeout(30 * time.Second),
    xphone.WithNATKeepalive(25 * time.Second),
    xphone.WithStunServer("stun.l.google.com:19302"),
    xphone.WithSRTP(),
    xphone.WithLogger(slog.Default()),
)
```

See the [Go documentation](https://pkg.go.dev/github.com/x-phone/xphone-go) for all options.

---

## NAT Traversal (STUN)

If your application runs behind NAT (most deployments), configure a STUN server so xphone can discover your public IP and advertise it correctly in SIP and SDP:

```go
phone := xphone.New(
    xphone.WithCredentials("1001", "secret", "sip.telnyx.com"),
    xphone.WithStunServer("stun.l.google.com:19302"),
)
```

When `StunServer` is set, xphone sends a STUN Binding Request at startup to learn your external IP. If the STUN server is unreachable, it falls back to local IP detection automatically.

Common public STUN servers:
- `stun.l.google.com:19302`
- `stun1.l.google.com:19302`
- `stun.cloudflare.com:3478`

---

## Opus Codec

Opus support is optional and requires CGO + libopus. The default build is CGO-free.

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
    xphone.WithCodecs(xphone.CodecOpus, xphone.CodecPCMU), // prefer Opus, fall back to PCMU
)
```

Opus runs at 8kHz natively — no resampling needed. PCM frames remain `[]int16`, mono, 160 samples (20ms), same as G.711. RTP timestamps use 48kHz clock per RFC 7587.

Without the `opus` build tag, `CodecOpus` is accepted in configuration but will not be negotiated (the codec processor returns nil, so SDP negotiation falls through to the next preferred codec).

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

## Working with Audio

xphone exposes audio as a stream of **PCM frames** through Go channels. Understanding the frame format and channel behaviour is key to building anything on top of the library.

### Frame format

Every frame is an `[]int16` with these fixed properties:

| Property | Value |
|---|---|
| Encoding | 16-bit signed PCM |
| Channels | Mono |
| Sample rate | 8000 Hz |
| Samples per frame | 160 |
| Frame duration | 20ms |

This is the native format expected by most speech-to-text APIs (Whisper, Deepgram, Google STT) and easily converted to `float32` for audio processing pipelines.

### Reading inbound audio

`call.PCMReader()` returns a `<-chan []int16`. Each receive gives you one 20ms frame of decoded audio from the remote party:

```go
go func() {
    for frame := range call.PCMReader() {
        // frame is []int16, 160 samples, 20ms of audio
        sendToSTT(frame)
    }
    // channel closes when the call ends
}()
```

> **Important:** Read frames promptly. The inbound buffer holds 256 frames (~5 seconds). If you fall behind, the oldest frames are silently dropped.

### Writing outbound audio

`call.PCMWriter()` returns a `chan<- []int16`. Send one 20ms frame at a time to transmit audio to the remote party:

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

> **Important:** Send frames at the natural 20ms pace. If you send faster than real-time, the outbound buffer fills and frames are dropped.

### Silence frame

```go
silence := make([]int16, 160) // zero-value is silence
call.PCMWriter() <- silence
```

### Converting to float32 (for ML pipelines)

Many audio and ML libraries expect `[]float32` normalized to `[-1.0, 1.0]`:

```go
func pcmToFloat32(frame []int16) []float32 {
    out := make([]float32, len(frame))
    for i, s := range frame {
        out[i] = float32(s) / 32768.0
    }
    return out
}
```

### Raw RTP access

For lower-level control — sending pre-encoded audio, implementing a custom codec, or inspecting RTP headers — use `RTPReader()` and `RTPWriter()` instead of the PCM channels:

```go
// Read raw RTP packets (post-jitter buffer, pre-decode)
go func() {
    for pkt := range call.RTPReader() {
        // pkt is *rtp.Packet (pion/rtp)
        processRTP(pkt)
    }
}()

// Write raw RTP packets (bypasses PCMWriter entirely)
call.RTPWriter() <- myRTPPacket
```

> Note: `RTPWriter` and `PCMWriter` are mutually exclusive — if you write to `RTPWriter`, `PCMWriter` is ignored for that call.

---

## Media Pipeline

```
Inbound:
  SIP Trunk -> RTP/UDP -> RTPRawReader (pre-jitter)
                        -> Jitter Buffer -> RTPReader (post-jitter)
                                          -> Codec Decode -> PCMReader ([]int16)

Outbound (mutually exclusive):
  PCMWriter -> Codec Encode -> RTP/UDP -> SIP Trunk
  RTPWriter -> RTP/UDP -> SIP Trunk   (raw mode — PCMWriter ignored)
```

All channels are buffered (256 entries). Inbound taps drop oldest on overflow; PCMWriter drops newest. Each frame is 160 samples at 8000 Hz = 20ms of audio.

The media pipeline runs on a dedicated goroutine per call, bridged to the rest of the application via Go channels.

---

## Call Control

```go
// Hold & resume
call.Hold()
call.Resume()

// Blind transfer
call.BlindTransfer("sip:1003@pbx.example.com")

// Mute (suppresses outbound audio, inbound still flows)
call.Mute()
call.Unmute()

// DTMF
call.SendDTMF("5")
call.OnDTMF(func(digit string) {
    fmt.Printf("Received: %s\n", digit)
})
```

---

## Testing

`MockPhone` and `MockCall` implement the `Phone` and `Call` interfaces — no real SIP server needed.

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

// Simulate inbound events
call.SimulateDTMF("9")
call.InjectRTP(pkt)
```

---

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

---

## Example App

`examples/sipcli` is a fully interactive terminal SIP client — registration, inbound/outbound calls, hold, resume, DTMF, mute, transfer, echo mode, and system speaker output:

```bash
# Using a profile from ~/.sipcli.yaml
cd examples/sipcli
go run . -profile myserver

# Direct flags
go run . -server pbx.example.com -user 1001 -pass secret
```

---

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

---

## Stack

| Layer | Implementation |
|---|---|
| SIP Signaling | [sipgo](https://github.com/emiago/sipgo) |
| RTP / SRTP | [pion/rtp](https://github.com/pion/rtp) + built-in SRTP (AES_CM_128_HMAC_SHA1_80) |
| G.711 / G.722 | Built-in (PCMU, PCMA) + [gotranspile/g722](https://github.com/gotranspile/g722) |
| Opus | [hraban/opus](https://github.com/hraban/opus) (optional, build tag `opus`) |
| RTCP | Built-in (RFC 3550 SR/RR) |
| Jitter Buffer | Built-in |
| STUN | Built-in (RFC 5389) |
| TUI (sipcli) | [bubbletea](https://github.com/charmbracelet/bubbletea) + [lipgloss](https://github.com/charmbracelet/lipgloss) |

---

## Known Limitations

This library is actively developed but not yet feature-complete. The gaps below are worth understanding before committing to it for a production deployment.

### Security

**SRTP uses SDES key exchange only.** The `AES_CM_128_HMAC_SHA1_80` cipher suite is supported with replay protection (RFC 3711 sliding window). DTLS-SRTP key exchange is not supported. Key material zeroization and SRTCP encryption are not yet implemented.

### Codec coverage

**Opus requires CGO and libopus.** Opus is supported but requires building with `-tags opus` and having `libopus-dev` installed. The default build is CGO-free. See [Opus Codec](#opus-codec) for details.

**G.729 is not supported.** G.729 remains widely deployed in enterprise PBX environments (Cisco, Avaya, Mitel). If your SIP trunk or PBX requires G.729, xphone cannot currently interoperate with it.

**PCM sample rate is fixed at 8 kHz (narrowband) or 16 kHz (G.722 wideband).** There is no configurable sample rate — codec selection determines the rate.

### Call control

**Attended (consultative) transfer is not implemented.** Only blind transfer via REFER is supported. Attended transfer requires coordinating two simultaneous call legs with a REFER/Replaces header.

**DTMF is RFC 4733 (RTP telephone-events) only.** Some legacy PBXes use SIP INFO (RFC 2976) for DTMF instead. If your system requires SIP INFO DTMF, tones may not be received.

**No call parking.** Park/retrieve functionality (common in office deployments) is not implemented.

### Enterprise features

**No MWI (Message Waiting Indicator).** SIP SUBSCRIBE/NOTIFY for the `message-summary` event package (RFC 3842) is not implemented. Applications cannot detect voicemail presence.

**No presence or BLF.** SIP SUBSCRIBE/NOTIFY for presence (RFC 3856) and dialog state (RFC 4235 — Busy Lamp Field) are not implemented.

**No SIP MESSAGE (RFC 3428).** Instant messaging over SIP is not supported.

### Network & NAT

**STUN is supported for NAT-mapped address discovery.** Configure `StunServer` to use a public STUN server (e.g. `stun.l.google.com:19302`) for discovering your external IP. STUN should only be used when the SIP server is on the public internet — do not enable it when connecting via VPN or private network, as the STUN-mapped address will be unreachable from the server.

**No TURN or ICE.** TURN relay (RFC 5766) and full ICE (RFC 5245) are not implemented. In environments with symmetric NAT (common in cloud VMs and corporate firewalls), STUN alone may not be sufficient and RTP media may fail to flow.

### Media

**No video.** Only audio media (single `m=audio` line in SDP) is supported. H.264, VP8, and other video codecs are not implemented.

---

## Roadmap

- DTLS-SRTP key exchange
- Attended (consultative) transfer
- SIP INFO DTMF (RFC 2976) for legacy PBX compatibility
- TURN relay and full ICE for symmetric NAT
- MWI (voicemail notification)

---

## License

MIT
