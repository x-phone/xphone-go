# sipgo Integration Plan

Adopt sipgo as the SIP signaling layer. Replace the stub `transport` and `phoneDialog` with real implementations backed by sipgo's dialog API. Keep unit test mocks, but reshape them to match sipgo's actual interfaces.

## Phase A: Foundation — sipgo dependency + new dialog interface

- [x] A1. Add `github.com/emiago/sipgo` to go.mod
- [x] A2. Define new `dialog` interface in `call.go` (error returns, `[]byte` SDP, unified `Respond`)
- [x] A3. Update `call.go` to use new dialog interface (method signatures change)
- [x] A4. Rebuild `testutil/MockDialog` to satisfy the new interface
- [x] A5. Fix all existing tests to compile with new interface
- [x] A6. Verify: `go test ./... -count=1` + `go test -race ./...` pass

## Phase B: sipgo wiring — registration

- [x] B1. Create `sipua.go` — thin wrapper (UA + Client, Server deferred to Phase D)
- [x] B2. Implement `phone.Connect()` using sipgo (`newSipUA()` replaces `newTransport()`)
- [x] B3. Registration via `sipUA.SendRequest("REGISTER")` using `client.Do()` — registry unchanged
- [x] B4. NAT keepalive stub (raw UDP access deferred to Phase E integration)
- [x] B5. Remove stub `transport` struct and `newTransport()` — keep `sipTransport` interface for tests
- [x] B6. `Connect()` creates `sipUA` directly
- [x] B7. Verify: `go test ./... -count=1` + `go test -race ./...` pass

## Phase C: sipgo wiring — outbound calls (Dial)

- [x] C1. Implement `phone.Dial()` using `dialogClient.Invite()`:
  - Build SDP offer via existing `internal/sdp`
  - `sess.WaitAnswer(ctx, AnswerOptions{OnResponse: ...})` for provisional handling
  - `sess.Ack(ctx)` on 200 OK
  - Create `sipgoDialog` wrapping `DialogClientSession`
  - Wire into `call` struct via `dialFn` pattern
- [x] C2. Implement `sipgoDialog` for outbound (UAC):
  - `SendBye(ctx)` → `sess.Bye(ctx)`
  - `SendCancel(ctx)` → cancel the WaitAnswer context
  - `SendReInvite(ctx, sdp)` → `sess.Do(ctx, reInviteReq)`
  - `SendRefer(ctx, target)` → build REFER, `sess.Do(ctx, referReq)`
  - Header accessors from `sess.InviteRequest` / `sess.InviteResponse`
- [x] C3. Handle dial timeout + context cancellation → CANCEL
  - sipgo's `WaitAnswer` sends CANCEL when context is cancelled
  - `dialFn` propagates context from `Dial()` (with timeout) through to `WaitAnswer`
- [x] C4. Verify: `go test ./... -count=1` + `go test -race ./...` pass

## Phase D: sipgo wiring — inbound calls

- [ ] D1. Implement `server.OnInvite` handler:
  - `dialogServer.ReadInvite(req, tx)` → creates `DialogServerSession`
  - Extract From/To, parse SDP offer
  - Create `sipgoDialog` wrapping `DialogServerSession`
  - Wire into `newInboundCall()`, fire `phone.OnIncoming`
- [ ] D2. Implement `sipgoDialog` for inbound (UAS):
  - `Respond(code, reason, body)` → `sess.Respond(code, reason, body)`
  - `SendBye(ctx)` → `sess.Bye(ctx)`
  - `SendReInvite(ctx, sdp)` → `sess.TransactionRequest(ctx, reInviteReq)`
  - `Ack(ctx)` → handled by sipgo internally for UAS
- [ ] D3. Wire `server.OnBye` → find call by dialog ID → `call.simulateBye()`
- [ ] D4. Wire `server.OnCancel` → find call by dialog ID → end call
- [ ] D5. Wire `server.OnAck` → `dialogServer.ReadAck(req, tx)`
- [ ] D6. Verify: `go test ./... -count=1` + `go test -race ./...` pass

## Phase E: integration tests with Docker/Asterisk

- [ ] E1. Write integration test: register extension 1001 with Asterisk
- [ ] E2. Write integration test: dial 1001 → 1002 (two xphone instances)
- [ ] E3. Write integration test: inbound call accept + BYE
- [ ] E4. Write integration test: hold/resume (re-INVITE)
- [ ] E5. Write integration test: DTMF send/receive
- [ ] E6. Write integration test: echo test (dial 9999, verify media)
- [ ] E7. Verify: `docker compose up` + `go test -tags=integration ./...`

## Phase F: cleanup

- [ ] F1. Update spec: remove `emiago/sipgo` from "Stack" (it's now a real dep, not just listed)
- [ ] F2. Update `docs/remaining-work.md`
- [ ] F3. Decide fate of `internal/sip/` — keep as test tooling or remove
- [ ] F4. Remove `connectWithTransport()` test hook if no longer needed
- [ ] F5. Update CLAUDE.md architecture section
- [ ] F6. Final review: `/simplify` + second-pass audit

## Key Design Decisions

**dialog interface shape**: The new `dialog` interface must accept `context.Context` on all network operations (BYE, CANCEL, re-INVITE, REFER) since sipgo is context-aware. The current interface has no context parameters — this is the main signature change.

**UAC vs UAS dialog**: `sipgoDialog` will have two concrete implementations (or one with a mode flag):
- UAC (`DialogClientSession`): created by `Dial()`
- UAS (`DialogServerSession`): created by `OnInvite` handler
Both satisfy the same `dialog` interface but delegate to different sipgo types.

**SDP handling**: sipgo passes SDP as `[]byte` in `Invite()` body and `Respond()` body. Our existing `internal/sdp` package builds SDP offers/answers — this stays.

**Call tracking**: `phone` needs a map of active calls by dialog ID so that `OnBye`/`OnCancel` handlers can route to the correct call. Currently there's no such map — calls are fire-and-forget. This needs to be added.

**MockTransport fate**: `MockTransport` is used by `registry_test.go` and `xphone_test.go`. Once registration uses sipgo directly, `MockTransport` becomes dead code. Registry tests will need to use sipgo's test infrastructure or a loopback transport.

## sipgo Key Types Reference

| sipgo type | xphone usage |
|---|---|
| `sipgo.UserAgent` | Created once in `Connect()` |
| `sipgo.Client` | Sending requests (REGISTER, in-dialog) |
| `sipgo.Server` | Receiving requests (INVITE, BYE, ACK, CANCEL) |
| `sipgo.DialogClient` | Managing outbound call dialogs |
| `sipgo.DialogServer` | Managing inbound call dialogs |
| `sipgo.DialogClientSession` | Per-call outbound session |
| `sipgo.DialogServerSession` | Per-call inbound session |
| `sip.Request` / `sip.Response` | SIP messages |
| `sip.ClientTransaction` | Outbound transaction (REGISTER) |
| `sip.ServerTransaction` | Inbound transaction (passed to handlers) |
