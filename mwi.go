package xphone

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultMWIExpires is the Expires value sent in SUBSCRIBE requests (RFC 3842).
const defaultMWIExpires = 600

// mwiSubscriber manages a SIP SUBSCRIBE dialog for Message Waiting Indication.
// It sends an initial SUBSCRIBE, refreshes periodically, and dispatches
// incoming NOTIFY bodies to the registered callback.
type mwiSubscriber struct {
	mu sync.Mutex

	tr           sipTransport
	voicemailURI string
	logger       *slog.Logger

	onVoicemailFn func(VoicemailStatus)

	ctx    context.Context
	cancel context.CancelFunc
}

func newMWISubscriber(tr sipTransport, voicemailURI string, logger *slog.Logger) *mwiSubscriber {
	return &mwiSubscriber{
		tr:           tr,
		voicemailURI: voicemailURI,
		logger:       logger,
	}
}

// start wires the NOTIFY handler and launches the background subscribe/refresh loop.
func (m *mwiSubscriber) start() {
	ctx, cancel := context.WithCancel(context.Background())

	m.mu.Lock()
	m.ctx = ctx
	m.cancel = cancel
	m.mu.Unlock()

	// Wire incoming MWI NOTIFY handler on transport.
	m.tr.OnMWINotify(m.handleNotify)

	// Launch background loop (initial subscribe + periodic refresh).
	go m.loop(ctx)
}

// stop cancels the background loop and sends a best-effort unsubscribe (Expires: 0).
func (m *mwiSubscriber) stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()

	// Cancel the background loop first.
	if cancel != nil {
		cancel()
	}

	// Best-effort unsubscribe with a short timeout.
	m.unsubscribe()
}

// setOnVoicemail sets the callback for voicemail status changes.
func (m *mwiSubscriber) setOnVoicemail(fn func(VoicemailStatus)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onVoicemailFn = fn
}

// mwiHeaders builds the common headers for MWI SUBSCRIBE requests.
func mwiHeaders(expires string) map[string]string {
	return map[string]string{
		"Event":   eventMWI,
		"Accept":  contentTypeMWI,
		"Expires": expires,
	}
}

// subscribe sends a SUBSCRIBE request with the configured Expires.
func (m *mwiSubscriber) subscribe(ctx context.Context) {
	code, _, err := m.tr.SendSubscribe(ctx, m.voicemailURI, mwiHeaders(strconv.Itoa(defaultMWIExpires)))
	if err != nil {
		m.logger.Warn("MWI SUBSCRIBE failed", "err", err)
		return
	}
	if code < 200 || code >= 300 {
		m.logger.Warn("MWI SUBSCRIBE rejected", "code", code)
		return
	}
	m.logger.Info("MWI subscribed", "uri", m.voicemailURI)
}

// unsubscribe sends a SUBSCRIBE with Expires: 0 to end the subscription.
func (m *mwiSubscriber) unsubscribe() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, err := m.tr.SendSubscribe(ctx, m.voicemailURI, mwiHeaders("0"))
	if err != nil {
		m.logger.Warn("MWI unsubscribe failed", "err", err)
	}
}

// loop sends the initial SUBSCRIBE and refreshes at half the Expires interval.
func (m *mwiSubscriber) loop(ctx context.Context) {
	// Initial SUBSCRIBE (best-effort).
	m.subscribe(ctx)

	refreshInterval := time.Duration(defaultMWIExpires/2) * time.Second
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.subscribe(ctx)
		}
	}
}

// handleNotify processes an incoming MWI NOTIFY body.
// Called from the transport's NOTIFY dispatch goroutine, so no extra go needed.
func (m *mwiSubscriber) handleNotify(body string) {
	status, ok := parseMessageSummary(body)
	if !ok {
		m.logger.Warn("MWI NOTIFY: failed to parse message-summary body")
		return
	}

	m.mu.Lock()
	fn := m.onVoicemailFn
	m.mu.Unlock()

	m.logger.Info("MWI status update",
		"waiting", status.MessagesWaiting,
		"new", status.NewMessages,
		"old", status.OldMessages,
	)

	if fn != nil {
		fn(status)
	}
}

// parseMessageSummary parses an application/simple-message-summary body (RFC 3842).
// Returns the parsed status and true, or zero-value and false if Messages-Waiting
// is not found.
func parseMessageSummary(body string) (VoicemailStatus, bool) {
	var status VoicemailStatus
	foundWaiting := false

	lines := strings.Split(body, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])

		if strings.EqualFold(key, "Messages-Waiting") {
			foundWaiting = true
			status.MessagesWaiting = strings.EqualFold(val, "yes")
		} else if strings.EqualFold(key, "Message-Account") {
			status.Account = val
		} else if strings.EqualFold(key, "Voice-Message") {
			status.NewMessages, status.OldMessages = parseMessageCounts(val)
		}
	}

	return status, foundWaiting
}

// parseMessageCounts parses "new/old" or "new/old (urgent_new/urgent_old)" counts.
func parseMessageCounts(s string) (int, int) {
	// Strip optional urgent counts: "2/8 (1/0)" → "2/8"
	if paren := strings.IndexByte(s, '('); paren >= 0 {
		s = strings.TrimSpace(s[:paren])
	}

	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, 0
	}

	newCount, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0
	}
	oldCount, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0
	}

	return newCount, oldCount
}
