# WithAuth Dial Option for Per-Call SIP Credentials

**Issue:** #90
**Date:** 2026-04-08

## Problem

Outbound SIP calls via `Phone.Dial()`, `Server.Dial()`, and `Server.DialURI()` cannot pass authentication credentials per-call. Credentials are only configurable at Phone creation time via `WithOutboundCredentials()` (or `WithCredentials()` fallback). This makes it impractical to dial through multiple SIP trunks with distinct proxy auth from a single Phone or Server instance.

## Solution

Add `WithAuth(username, password string)` as a `DialOption` that overrides config-level credentials for a single call.

## API Surface

Two new fields on `DialOptions` and one new constructor in `options.go`:

```go
type DialOptions struct {
    // ... existing fields ...
    AuthUsername string // per-call SIP digest username (overrides config)
    AuthPassword string // per-call SIP digest password (overrides config)
}

func WithAuth(username, password string) DialOption {
    return func(o *DialOptions) {
        o.AuthUsername = username
        o.AuthPassword = password
    }
}
```

Usage:

```go
// Phone path
call, err := phone.Dial(ctx, target,
    xphone.WithAuth("trunk-user", "trunk-pass"),
)

// Server path
call, err := server.DialURI(ctx, uri, from,
    xphone.WithAuth("proxy-user", "proxy-pass"),
)
```

## Credential Resolution

### Phone path (`sipUA.dialOnce` in `sipua.go`)

Precedence chain (first non-empty wins):

1. `DialOptions.AuthUsername` / `DialOptions.AuthPassword`
2. `Config.OutboundUsername` / `Config.OutboundPassword`
3. `Config.Username` / `Config.Password`

The existing fallback logic at `sipua.go:293-301` is preserved. The only change is prepending a check for `opts.AuthUsername` before the current `OutboundUsername` check.

### Server path (`server.dialOnce` in `server.go`)

Currently passes no credentials to `WaitAnswer` at all (line 868). Change to:

1. `DialOptions.AuthUsername` / `DialOptions.AuthPassword`
2. No fallback (Server has no registration credentials)

If `WithAuth` is not provided, behavior is unchanged (no auth).

## Files Changed

| File | Change |
|------|--------|
| `options.go` | Add `AuthUsername`, `AuthPassword` fields to `DialOptions`; add `WithAuth()` constructor |
| `sipua.go` | In `dialOnce()`, prepend `DialOptions` auth check before existing fallback chain |
| `server.go` | In `dialOnce()`, pass `DialOptions` auth credentials to `WaitAnswer` |

## Testing

- Unit test: `WithAuth` sets `AuthUsername`/`AuthPassword` on `DialOptions`
- Unit test: credential precedence — per-call overrides outbound overrides registration
- Unit test: `server.dialOnce` passes auth to `WaitAnswer` when `WithAuth` is provided

## Design Decisions

- **Inline fields vs pointer struct:** Inline `string` fields on `DialOptions` chosen over `*DialAuth` struct. Matches existing option patterns (all fields are value types), avoids allocation, and empty string already means "not set" in the existing resolution logic.
- **Per-call always wins:** If `WithAuth` is provided, those credentials are used regardless of what's configured at phone/config level. This matches the Rust implementation and is the only behavior that makes the feature useful in the primary use case (phone with default trunk credentials dialing a different trunk).
- **Server has no default auth:** Unlike Phone, Server has no registration credentials to fall back to. `WithAuth` is the only way to provide credentials for server-initiated calls.
