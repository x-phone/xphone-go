# WithAuth Dial Option Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `WithAuth(username, password)` DialOption for per-call SIP digest credentials (issue #90).

**Architecture:** Two new string fields on `DialOptions`, one new constructor `WithAuth()`, and credential resolution changes in both `sipUA.dialOnce` (Phone path) and `server.dialOnce` (Server path). Precedence: per-call > outbound config > registration config.

**Tech Stack:** Go, stdlib only (no new dependencies)

---

### Task 1: Add WithAuth to DialOptions

**Files:**
- Modify: `options.go:106-114` (add fields to `DialOptions`)
- Modify: `options.go:166` (add `WithAuth` constructor after `WithVideo`)

- [ ] **Step 1: Write the failing test**

Create `options_test.go` with a test that verifies `WithAuth` sets both fields:

```go
package xphone

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWithAuth_SetsCredentials(t *testing.T) {
	opts := applyDialOptions([]DialOption{
		WithAuth("trunk-user", "trunk-pass"),
	})
	assert.Equal(t, "trunk-user", opts.AuthUsername)
	assert.Equal(t, "trunk-pass", opts.AuthPassword)
}

func TestWithAuth_DefaultsEmpty(t *testing.T) {
	opts := applyDialOptions(nil)
	assert.Empty(t, opts.AuthUsername)
	assert.Empty(t, opts.AuthPassword)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestWithAuth -v ./...`
Expected: compile error — `AuthUsername` and `WithAuth` not defined.

- [ ] **Step 3: Add AuthUsername/AuthPassword fields to DialOptions**

In `options.go`, add two fields to the `DialOptions` struct (after `VideoCodecs`):

```go
type DialOptions struct {
	CallerID      string
	CustomHeaders map[string]string
	EarlyMedia    bool
	Timeout       time.Duration
	CodecOverride []Codec
	Video         bool         // enable video in SDP offer
	VideoCodecs   []VideoCodec // video codec preferences (default: [H264, VP8])
	AuthUsername   string       // per-call SIP digest username (overrides config)
	AuthPassword   string       // per-call SIP digest password (overrides config)
}
```

- [ ] **Step 4: Add WithAuth constructor**

In `options.go`, add after `WithVideo`:

```go
// WithAuth sets per-call SIP digest credentials for 401/407 proxy
// authentication challenges. Overrides phone-level WithOutboundCredentials
// and WithCredentials for this single dial attempt.
func WithAuth(username, password string) DialOption {
	return func(o *DialOptions) {
		o.AuthUsername = username
		o.AuthPassword = password
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test -run TestWithAuth -v ./...`
Expected: PASS

- [ ] **Step 6: Format and commit**

```bash
gofmt -s -w options.go options_test.go
git add options.go options_test.go
git commit -m "feat: add WithAuth dial option to DialOptions (#90)"
```

---

### Task 2: Wire WithAuth into Phone credential resolution

**Files:**
- Modify: `sipua.go:293-301` (prepend per-call auth check)
- Test: `options_test.go` (add precedence tests)

- [ ] **Step 1: Write the failing tests**

Append to `options_test.go`:

```go
func TestResolveAuthCredentials_PerCallOverridesAll(t *testing.T) {
	cfg := Config{
		Username:         "reg-user",
		Password:         "reg-pass",
		OutboundUsername: "outbound-user",
		OutboundPassword: "outbound-pass",
	}
	opts := DialOptions{
		AuthUsername: "call-user",
		AuthPassword: "call-pass",
	}
	user, pass := resolveAuthCredentials(opts, cfg)
	assert.Equal(t, "call-user", user)
	assert.Equal(t, "call-pass", pass)
}

func TestResolveAuthCredentials_OutboundOverridesRegistration(t *testing.T) {
	cfg := Config{
		Username:         "reg-user",
		Password:         "reg-pass",
		OutboundUsername: "outbound-user",
		OutboundPassword: "outbound-pass",
	}
	opts := DialOptions{}
	user, pass := resolveAuthCredentials(opts, cfg)
	assert.Equal(t, "outbound-user", user)
	assert.Equal(t, "outbound-pass", pass)
}

func TestResolveAuthCredentials_FallsBackToRegistration(t *testing.T) {
	cfg := Config{
		Username: "reg-user",
		Password: "reg-pass",
	}
	opts := DialOptions{}
	user, pass := resolveAuthCredentials(opts, cfg)
	assert.Equal(t, "reg-user", user)
	assert.Equal(t, "reg-pass", pass)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run TestResolveAuth -v ./...`
Expected: compile error — `resolveAuthCredentials` not defined.

- [ ] **Step 3: Extract resolveAuthCredentials helper and wire it into dialOnce**

In `sipua.go`, replace lines 293-301 with a call to a new helper. Add the helper function:

```go
// resolveAuthCredentials returns the SIP digest credentials to use for an
// outbound INVITE. Precedence: per-call WithAuth > config OutboundCredentials > registration credentials.
func resolveAuthCredentials(opts DialOptions, cfg Config) (string, string) {
	user := opts.AuthUsername
	if user == "" {
		user = cfg.OutboundUsername
	}
	if user == "" {
		user = cfg.Username
	}
	pass := opts.AuthPassword
	if pass == "" {
		pass = cfg.OutboundPassword
	}
	if pass == "" {
		pass = cfg.Password
	}
	return user, pass
}
```

Then in `sipua.dialOnce`, replace the credential resolution block (lines 293-301) with:

```go
	// Resolve outbound credentials: per-call WithAuth > OutboundCredentials > registration.
	authUser, authPass := resolveAuthCredentials(opts, s.cfg)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -run "TestResolveAuth|TestWithAuth" -v ./...`
Expected: PASS (all 5 tests)

- [ ] **Step 5: Run full test suite**

Run: `go test ./...`
Expected: PASS (no regressions)

- [ ] **Step 6: Format and commit**

```bash
gofmt -s -w sipua.go options_test.go
git add sipua.go options_test.go
git commit -m "feat: wire WithAuth into Phone credential resolution (#90)"
```

---

### Task 3: Wire WithAuth into Server credential resolution

**Files:**
- Modify: `server.go:868-875` (pass auth credentials to WaitAnswer)

- [ ] **Step 1: Write the failing test**

Append to `options_test.go`:

```go
func TestResolveAuthCredentials_EmptyConfigNoAuth(t *testing.T) {
	cfg := Config{}
	opts := DialOptions{}
	user, pass := resolveAuthCredentials(opts, cfg)
	assert.Empty(t, user)
	assert.Empty(t, pass)
}

func TestResolveAuthCredentials_PerCallOnlyNoConfig(t *testing.T) {
	cfg := Config{}
	opts := DialOptions{
		AuthUsername: "call-user",
		AuthPassword: "call-pass",
	}
	user, pass := resolveAuthCredentials(opts, cfg)
	assert.Equal(t, "call-user", user)
	assert.Equal(t, "call-pass", pass)
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test -run "TestResolveAuthCredentials_Empty|TestResolveAuthCredentials_PerCallOnly" -v ./...`
Expected: PASS (the helper already handles this correctly — these tests document the Server use case)

- [ ] **Step 3: Wire resolveAuthCredentials into server.dialOnce**

In `server.go`, the `dialOnce` method at line 868 passes no credentials to `WaitAnswer`. Server has no registration config, so we pass an empty `Config{}` — only per-call `WithAuth` credentials will be used. Replace lines 868-875:

```go
	// Resolve per-call auth credentials (Server has no config-level fallback).
	authUser, authPass := resolveAuthCredentials(opts, Config{})

	err = sess.WaitAnswer(waitCtx, sipgo.AnswerOptions{
		OnResponse: func(res *sip.Response) error {
			if onResponse != nil {
				onResponse(res.StatusCode, res.Reason)
			}
			return nil
		},
		Username: authUser,
		Password: authPass,
	})
```

- [ ] **Step 4: Run full test suite**

Run: `go test ./...`
Expected: PASS (no regressions — existing server tests don't use auth, so behavior is unchanged when WithAuth is not provided)

- [ ] **Step 5: Format and commit**

```bash
gofmt -s -w server.go options_test.go
git add server.go options_test.go
git commit -m "feat: wire WithAuth into Server credential resolution (#90)"
```

---

### Task 4: Update CHANGELOG.md

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add entry under Unreleased**

Add under `## Unreleased` in the `Features` section:

```markdown
- **WithAuth dial option**: per-call SIP digest credentials via `WithAuth(username, password)` for 401/407 proxy authentication. Overrides phone-level `WithOutboundCredentials` for individual `Dial()` calls. Works with both `Phone.Dial()` and `Server.Dial()`/`Server.DialURI()`. (#90)
```

- [ ] **Step 2: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: add WithAuth to CHANGELOG (#90)"
```
