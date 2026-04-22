# Changelog

## Unreleased

### Bug fixes
- `WithRTPAddress` no longer rewrites the SIP Contact/Via host when `Config.NAT` is also set. Previously the override replaced both the SDP `c=` line (media) and the Contact URI (signaling), but the SIP client's bound UDP port is ephemeral — advertising a reachable-looking Contact host while the signaling port is not publishable caused the PBX to target an unpublished port and drop inbound INVITEs (Docker-bridge local dev, Kubernetes NAT). With the fix, Contact/Via fall back to the real bind interface IP when `Config.NAT` is set, so rport (RFC 3581) + REGISTER keepalive continue to drive the PBX's NAT learning. Deployments without `Config.NAT` (direct public VPS) are unaffected. (#109)

## v0.6.1

### Features
- `WithRTPAddress(ip)` PhoneOption (plus `Config.RTPAddress`) — overrides the IPv4 literal advertised in SIP Contact headers, the SDP `c=` line, and the ICE host candidate. Takes precedence over STUN discovery and `localIPFor()` auto-detection. Mirrors the existing `ServerConfig.RTPAddress` on the trunk side. Required for local dev against a LAN PBX when the kernel's outbound interface lookup picks an IP that isn't routable from the peer — Docker container IP, VPN tunnel, multi-homed host, or WSL2 vEthernet. (#105)

### Bug fixes
- `Config.RTPAddress`, `ServerConfig.RTPAddress`, and `PeerConfig.RTPAddress` now validate their input as IPv4 literals at construction. Non-IPv4 values (IPv6, hostnames, whitespace, embedded CRLF) are rejected with a log warning and treated as unset. Previously, unvalidated strings flowed verbatim into SDP `c=` lines and SIP Contact headers, exposing a CRLF header-injection surface. Applies symmetrically to both Phone and Server configs.

## v0.6.0

### Bug fixes
- REGISTER failure now surfaces the last observed SIP response code and reason via a typed `*RegistrationFailedError{Code, Reason, TransportErr}` returned from `Phone.Connect()` and delivered to `Phone.OnError`. The final error log includes `last_code`, `last_reason`, and `last_err` (omitted when nil) — previously the library logged only `registration failed` with no actionable detail, forcing tcpdump-level diagnosis. Per-attempt retries now log at debug level with attempt number, code, and reason. (#96)
  - `errors.Is(err, ErrRegistrationFailed)` and `errors.Is(err, regErr.TransportErr)` both still match via `Unwrap() []error`.
  - Minor compatibility note: callers that relied on **direct equality** (`err == ErrRegistrationFailed`) or `errors.Unwrap(err)` returning the sentinel must switch to `errors.Is`. `errors.Is` has been the recommended form since Go 1.13.

### Breaking changes
- `WithOutboundProxy` / `Config.OutboundProxy` now routes **all** outbound SIP requests (REGISTER, SUBSCRIBE, MESSAGE, INVITE) through the proxy, not just INVITE. Matches how Kamailio / OpenSIPS / Asterisk outbound-proxy deployments expect a single next-hop for all signaling. Migration: for most deployments no action is needed — the proxy was already the expected next-hop. Callers who genuinely needed the prior "INVITE via proxy, REGISTER direct" split can either drop `OutboundProxy` (all requests go direct to `Host`) or run two `Phone` instances, one for registration and one for outbound calls.

### Features
- `WithNAT()` PhoneOption (plus `Config.NAT` / `ServerConfig.NAT`) — enables RFC 3581 rport on outgoing UDP SIP requests so peers reply to the NAT-mapped source IP:port rather than the sent-by Via address. Required for phones/servers behind NAT (dev environments, containerized deployments) where the PBX would otherwise reply to an unreachable port.

## v0.5.12

### Features
- `Config.AuthUsername` and `WithAuthUsername(username)` — default digest Authorization username used across REGISTER, SUBSCRIBE, MESSAGE, and outbound INVITE, separate from the SIP AOR carried in From/To/Contact. Needed for PBXes where the authentication identity differs from the extension (e.g. 3CX trunks with a distinct Authentication ID). More specific overrides still win (`OutboundUsername` for INVITE, per-call `WithAuth` for `Dial`). Falls back to `Username` when unset; behavior unchanged for existing setups.

## v0.5.11

### Features
- `WithAuth(username, password)` dial option — pass per-call SIP credentials for both `Phone` and `Server` outbound calls, overriding any config-level registration credentials (#90)

## v0.5.10

### Bug fixes
- `Call.Accept()` no longer blocks up to 32 seconds waiting for ACK — the 200 OK is sent immediately and the RFC 3261 §13.3.1.4 retransmit loop runs in the background, matching the Rust version's non-blocking behavior

## v0.5.9

### Features
- `Server.OnOptions(func() int)` — callback to control SIP OPTIONS responses; return a status code (e.g. 200 healthy, 503 draining) for SIP proxy health checks (Kamailio, OpenSIPS). Default: 200 OK.

## v0.5.8

### Features
- `ServerConfig.Listener` accepts a pre-created `net.PacketConn` for the SIP UDP socket — gives callers full control over socket options (e.g. `SO_REUSEPORT`, buffer sizes) and socket lifecycle (e.g. fd passing for zero-downtime deploys)

## v0.5.7

### Bug fixes
- Add `User-Agent` header to SIP requests — sipgo does not add it automatically; uses `DisplayName` if set, falls back to `"xphone"`

## v0.5.6

### Bug fixes
- Force IPv4 (`udp4`) for SIP signaling transport and address resolution — fixes registration failure on dual-stack hosts where Go's resolver picks IPv6 but PBX only listens on IPv4 (#79)
- Reduce un-REGISTER timeout from 3s to 500ms — fixes registration failure on hot-reload where pending un-REGISTER transaction raced with transport teardown

## v0.5.5

### Features
- `WithDisplayName(name)` sets the display name in SIP Contact headers — helps PBXes identify the device (#75)

## v0.5.4

### Bug fixes
- `Phone.Disconnect()` now sends REGISTER with `Expires: 0` (un-REGISTER) to the registrar before closing transport, per RFC 3261 §10.2.2 — previously left stale contacts on the PBX (#70)
- `On*` callback setters on `Call`, `Phone`, and `Server` now append instead of replace — calling `OnEnded` (or any other `On*` method) multiple times registers all callbacks instead of silently dropping earlier ones (#71)

## v0.5.3

### Features
- `Call.AttendedTransfer(other Call)` — attended transfer now lives on the Call interface, works for both Phone and Server calls (#67)
- `Phone.AttendedTransfer(callA, callB)` remains as a thin delegate (non-breaking)

## v0.5.2

### Features
- `Server.DialURI(ctx, uri, from, opts...)` initiates outbound calls to arbitrary SIP URIs without requiring a pre-configured peer (#64)

## v0.5.1

### Bug fixes
- Fix Hold/Resume re-INVITE: send ACK after 200 OK per RFC 3261 §13.2.2.4 — previously the client transaction was destroyed before the 200 OK arrived, leaving the dialog in an undefined state (#61)
- Hold() and Resume() now return errors from the re-INVITE instead of silently ignoring failures

## v0.5.0

### Features
- `ReplaceAudioWriter(newSrc <-chan []int16)` atomically swaps the audio input channel on an active call — enables in-app music on hold without audio bleed (#58)
- Outbound proxy support: `WithOutboundProxy("sip:proxy.example.com:5060")` routes INVITEs through a proxy
- Separate outbound credentials: `WithOutboundCredentials("trunk-user", "trunk-pass")` for INVITE auth
- `WithHeader()` / `DialOptions.CustomHeaders` now applied to outbound INVITEs (previously ignored)

## v0.4.5

### Bug fixes
- `BlindTransfer` and `AttendedTransfer` now surface REFER failures via `EndedByTransferFailed` end reason — previously failure NOTIFYs (>= 300) were silently ignored (#54)

## v0.4.4

### Bug fixes
- Fix in-dialog request routing (REFER, re-INVITE, INFO) for inbound UAS calls — use remote party's Contact address per RFC 3261 §12.2.1.1 instead of the original INVITE Request-URI, which looped back to the server itself (#51)

## v0.4.3

### Bug fixes
- Server mode: SRTP now covers video streams (previously only audio contexts were initialized)
- `hasURIParam` is now case-insensitive per RFC 3261 §19.1.1

### Internal
- Deduplicate ~120 lines of shared logic across phone/server and audio/video paths
- Extract shared helpers: `mediaStream.reset()`, `startRTCPReader()`, `startRTPReaderFor()`, `wireCallCallbacks()`, `extractHostPort()`, `call.setupSRTP()`

## v0.4.2

### Bug fixes
- Server mode: include listening port in SIP Contact header — fixes ACK routing to non-default ports (e.g., 5080)

## v0.4.1

### Bug fixes
- Force IPv4 (`udp4`) for all RTP/RTCP sockets and address resolution — fixes zero outbound RTP on Linux dual-stack hosts
- Log `WriteTo` errors on RTP, RTCP (first failure per stream), and DTMF sends for diagnosability
- Log total RTP/RTCP send error counts when media pipeline exits
- Log STUN binding response `WriteTo` errors
- `Connect()` now returns an error when SIP registration fails (previously always returned nil)
- "phone connected" log no longer emits on registration failure
- `WithCredentials` now accepts `host:port` format (e.g. `"10.0.0.7:5060"`) — previously caused malformed SIP URIs

## v0.4.0

### Server mode
- `Server` connection mode: accept and place calls directly with trusted SIP peers without registration
- Peer authentication: IP/CIDR matching + SIP digest auth (RFC 2617)
- Per-peer RTP address and codec overrides
- Stale call reaper (30s setup TTL, 4h active TTL)
- FakePBX integration tests for inbound/outbound calls, RTP, DTMF, peer rejection

### Docs
- Add Connection Modes section to README (Phone vs Server)
- Add Server mode to features table
- Add PacedPCMWriter documentation to README
- Add RTP port range documentation to README

## v0.3.2

### Media
- `PacedPCMWriter()` channel for TTS burst audio — auto-frames and paces arbitrary-length PCM at 20ms intervals

### Bug fixes
- Fix concurrent inbound calls getting `m=audio 0` in SDP by eagerly allocating RTP port

### CI
- Add changelog check: PRs now require a CHANGELOG.md entry

### Tests
- FakePBX integration tests for multiple simultaneous calls and PacedPCMWriter

## v0.3.1

### Internal
- Reduce cyclomatic complexity in hotspot functions

## v0.3.0

### Video
- H.264 (RFC 6184) and VP8 (RFC 7741) packetizers/depacketizers
- Video RTP pipeline with VideoReader / VideoWriter / VideoRTPReader / VideoRTPWriter
- Mid-call video upgrade and downgrade via re-INVITE
- RTCP PLI/FIR for keyframe requests
- Video SRTP (separate contexts for audio and video)
- OnVideo callback fires for all video activation paths

### Audio codecs
- G.729 codec support via bcg729 (build tag `g729`)

### Call control
- Attended transfer (RFC 3891 Replaces)
- SIP INFO DTMF (RFC 2976) with configurable DtmfMode
- Call waiting with `Phone.Calls()` API

### Messaging & presence
- SIP MESSAGE instant messaging (RFC 3428)
- SIP SUBSCRIBE/NOTIFY (RFC 6665)
- MWI / voicemail notification (RFC 3842)
- BLF / Busy Lamp Field monitoring (RFC 4235)
- SIP Presence (RFC 3856)

### Network
- TURN relay for symmetric NAT (RFC 5766)
- ICE-Lite (RFC 8445 §2.2)

### Security
- SRTCP encryption (RFC 3711 §3.4)
- Key material zeroization

### Internal
- Extract media pipeline into per-stream mediaStream abstraction
- Video SDP support with multi-media m= lines (RFC 3264)
- Single dispatch goroutine per call (replaces per-callback goroutines)
- Hold media timeout enforcement (5 minutes)
- Fix stale SRTP context after re-INVITE

## v0.2.0

- 302 redirect following for INVITE
- SRTP replay protection (RFC 3711 §3.3.2)
- RTCP Sender/Receiver Reports (RFC 3550)
- Opus codec support (build tag `opus`)

## v0.1.0

- SIP registration with auth, keepalive, auto-reconnect
- Inbound and outbound calls
- Hold / Resume via re-INVITE
- Blind transfer (REFER)
- Mute / Unmute
- Early media (183 Session Progress)
- Session timers (RFC 4028)
- G.711 (PCMU, PCMA) and G.722 codecs
- PCM audio frames and raw RTP access
- Jitter buffer
- SRTP (AES_CM_128_HMAC_SHA1_80) with SDES key exchange
- STUN NAT traversal (RFC 5389)
- TCP and TLS SIP transport
- MockPhone and MockCall for unit testing
- FakePBX integration tests
- sipcli interactive terminal client
