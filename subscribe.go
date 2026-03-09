package xphone

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultSubscribeExpires is the default Expires value for SUBSCRIBE requests.
const defaultSubscribeExpires = 600

// RFC 6665 §4.1.3 Subscription-State reason values that trigger auto-resubscribe.
const (
	reasonDeactivated = "deactivated"
	reasonTimeout     = "timeout"
)

// subscription represents a single SIP SUBSCRIBE dialog.
type subscription struct {
	id      string
	uri     string
	event   string
	expires int

	// Watch-specific fields.
	isWatch   bool
	extension string
	watchFn   func(ext string, state ExtensionState, prev ExtensionState)
	prevState ExtensionState

	// SubscribeEvent-specific fields.
	notifyFn func(NotifyEvent)

	// Refresh goroutine cancel.
	refreshCancel context.CancelFunc
}

// subscriptionManager manages SUBSCRIBE/NOTIFY dialogs.
// It handles BLF Watch subscriptions (dialog-info) and generic SubscribeEvent.
type subscriptionManager struct {
	mu     sync.Mutex
	tr     sipTransport
	host   string
	logger *slog.Logger
	subs   map[string]*subscription
	ctx    context.Context
	cancel context.CancelFunc
}

func newSubscriptionManager(tr sipTransport, host string, logger *slog.Logger) *subscriptionManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &subscriptionManager{
		tr:     tr,
		host:   host,
		logger: logger,
		subs:   make(map[string]*subscription),
		ctx:    ctx,
		cancel: cancel,
	}
}

// start wires the general NOTIFY handler on the transport.
func (m *subscriptionManager) start() {
	m.tr.OnNotify(m.handleNotify)
}

// stop cancels all refresh goroutines and sends best-effort unsubscribes.
func (m *subscriptionManager) stop() {
	m.cancel()

	m.mu.Lock()
	subs := make([]*subscription, 0, len(m.subs))
	for _, s := range m.subs {
		subs = append(subs, s)
	}
	m.subs = make(map[string]*subscription)
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, s := range subs {
		if s.refreshCancel != nil {
			s.refreshCancel()
		}
		wg.Add(1)
		go func(sub *subscription) {
			defer wg.Done()
			m.sendUnsubscribe(sub)
		}(s)
	}
	wg.Wait()
}

// watch creates a BLF subscription for an extension using dialog event (RFC 4235).
// Returns a subscription ID that can be passed to unwatch.
func (m *subscriptionManager) watch(ctx context.Context, extension string, fn func(string, ExtensionState, ExtensionState)) (string, error) {
	sub := &subscription{
		id:        newCallID(),
		uri:       "sip:" + extension + "@" + m.host,
		event:     eventDialog,
		expires:   defaultSubscribeExpires,
		isWatch:   true,
		extension: extension,
		watchFn:   fn,
		prevState: ExtensionUnknown,
	}
	return m.doSubscribe(ctx, sub)
}

// unwatch removes a BLF Watch subscription.
func (m *subscriptionManager) unwatch(id string) error {
	return m.remove(id)
}

// subscribeEvent creates a generic SIP event subscription.
// Returns a subscription ID that can be passed to unsubscribeEvent.
func (m *subscriptionManager) subscribeEvent(ctx context.Context, uri, event string, expires int, fn func(NotifyEvent)) (string, error) {
	if expires <= 0 {
		expires = defaultSubscribeExpires
	}
	sub := &subscription{
		id:       newCallID(),
		uri:      uri,
		event:    event,
		expires:  expires,
		notifyFn: fn,
	}
	return m.doSubscribe(ctx, sub)
}

// doSubscribe registers a subscription and sends the initial SUBSCRIBE request.
// On failure, the subscription is removed from the map.
func (m *subscriptionManager) doSubscribe(ctx context.Context, sub *subscription) (string, error) {
	// Register before sending so the initial NOTIFY can be dispatched.
	m.mu.Lock()
	m.subs[sub.id] = sub
	m.mu.Unlock()

	headers := m.subscribeHeaders(sub)
	code, _, err := m.tr.SendSubscribe(ctx, sub.uri, headers)
	if err != nil || code >= 400 {
		m.mu.Lock()
		delete(m.subs, sub.id)
		m.mu.Unlock()
		if err != nil {
			return "", err
		}
		return "", ErrSubscriptionRejected
	}

	m.startRefresh(sub)
	return sub.id, nil
}

// unsubscribeEvent removes a generic event subscription.
func (m *subscriptionManager) unsubscribeEvent(id string) error {
	return m.remove(id)
}

// remove cancels a subscription by ID: stops refresh and sends Expires: 0.
func (m *subscriptionManager) remove(id string) error {
	m.mu.Lock()
	sub, ok := m.subs[id]
	if !ok {
		m.mu.Unlock()
		return ErrSubscriptionNotFound
	}
	delete(m.subs, id)
	m.mu.Unlock()

	if sub.refreshCancel != nil {
		sub.refreshCancel()
	}
	m.sendUnsubscribe(sub)
	return nil
}

// subscribeHeaders builds the common headers for a SUBSCRIBE request.
func (m *subscriptionManager) subscribeHeaders(sub *subscription) map[string]string {
	headers := map[string]string{
		"Event":   sub.event,
		"Expires": strconv.Itoa(sub.expires),
	}
	if sub.isWatch {
		headers["Accept"] = contentTypeDialogInfo
	}
	return headers
}

// sendUnsubscribe sends a SUBSCRIBE with Expires: 0 (best-effort).
func (m *subscriptionManager) sendUnsubscribe(sub *subscription) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	headers := map[string]string{
		"Event":   sub.event,
		"Expires": "0",
	}
	m.tr.SendSubscribe(ctx, sub.uri, headers)
}

// startRefresh launches a goroutine that refreshes the subscription at half the Expires interval.
func (m *subscriptionManager) startRefresh(sub *subscription) {
	refreshCtx, refreshCancel := context.WithCancel(m.ctx)

	m.mu.Lock()
	sub.refreshCancel = refreshCancel
	m.mu.Unlock()

	interval := time.Duration(sub.expires/2) * time.Second
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-refreshCtx.Done():
				return
			case <-ticker.C:
				m.refresh(refreshCtx, sub)
			}
		}
	}()
}

// refresh sends a SUBSCRIBE refresh for the given subscription.
func (m *subscriptionManager) refresh(ctx context.Context, sub *subscription) {
	headers := m.subscribeHeaders(sub)
	code, _, err := m.tr.SendSubscribe(ctx, sub.uri, headers)
	if err != nil {
		m.logger.Warn("subscription refresh failed", "id", sub.id, "err", err)
		return
	}
	if code >= 400 {
		m.logger.Warn("subscription refresh rejected", "id", sub.id, "code", code)
		m.mu.Lock()
		delete(m.subs, sub.id)
		m.mu.Unlock()
		if sub.refreshCancel != nil {
			sub.refreshCancel()
		}
	}
}

// handleNotify processes incoming NOTIFY requests from the transport.
func (m *subscriptionManager) handleNotify(event, from, contentType, body, subscriptionState string) {
	subState, stateExpires, reason := parseSubscriptionState(subscriptionState)

	m.mu.Lock()
	match := m.findMatch(event, from)
	if match == nil {
		m.mu.Unlock()
		return
	}

	// Snapshot callback fields under lock.
	watchFn := match.watchFn
	notifyFn := match.notifyFn
	isWatch := match.isWatch
	extension := match.extension
	prev := match.prevState

	// Handle terminated subscriptions under lock.
	var cancelFn context.CancelFunc
	if subState == SubStateTerminated {
		if reason == reasonDeactivated || reason == reasonTimeout {
			// Will resubscribe after unlock.
		} else {
			delete(m.subs, match.id)
			cancelFn = match.refreshCancel
		}
	}

	// Update refresh interval if server changed expires.
	restartRefresh := false
	if stateExpires > 0 && stateExpires != match.expires {
		match.expires = stateExpires
		cancelFn = match.refreshCancel
		restartRefresh = true
	}
	m.mu.Unlock()

	if cancelFn != nil {
		cancelFn()
	}
	if subState == SubStateTerminated && (reason == reasonDeactivated || reason == reasonTimeout) {
		go m.resubscribe(match)
	}
	if restartRefresh {
		m.startRefresh(match)
	}

	// Dispatch to callback.
	if isWatch && watchFn != nil {
		state, err := parseDialogInfo(body)
		if err != nil {
			m.logger.Warn("watch: failed to parse dialog-info", "ext", extension, "err", err)
			return
		}
		m.mu.Lock()
		match.prevState = state
		m.mu.Unlock()
		watchFn(extension, state, prev)
	} else if notifyFn != nil {
		notifyFn(NotifyEvent{
			Event:             event,
			ContentType:       contentType,
			Body:              body,
			SubscriptionState: subState,
			Expires:           stateExpires,
			Reason:            reason,
		})
	}
}

// findMatch finds a subscription matching the given event and from URI.
// Must be called with m.mu held.
func (m *subscriptionManager) findMatch(event, from string) *subscription {
	fromUser := extractSIPUser(from)
	for _, s := range m.subs {
		if s.event != event {
			continue
		}
		if s.isWatch {
			if fromUser == s.extension {
				return s
			}
		} else {
			if extractSIPUser(s.uri) == fromUser {
				return s
			}
		}
	}
	return nil
}

// resubscribe sends a fresh SUBSCRIBE after a deactivated/timeout termination.
func (m *subscriptionManager) resubscribe(sub *subscription) {
	if sub.refreshCancel != nil {
		sub.refreshCancel()
	}

	// Check that the subscription is still active (not unwatched by user).
	m.mu.Lock()
	if _, ok := m.subs[sub.id]; !ok {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()

	headers := m.subscribeHeaders(sub)
	code, _, err := m.tr.SendSubscribe(ctx, sub.uri, headers)
	if err != nil || code >= 400 {
		m.logger.Warn("resubscribe failed, removing subscription", "id", sub.id)
		m.mu.Lock()
		delete(m.subs, sub.id)
		m.mu.Unlock()
		return
	}

	m.startRefresh(sub)
}

// parseSubscriptionState parses a Subscription-State header value.
// Examples: "active;expires=600", "terminated;reason=deactivated"
func parseSubscriptionState(header string) (SubState, int, string) {
	if header == "" {
		return SubStateActive, 0, ""
	}

	parts := strings.Split(header, ";")
	stateStr := strings.TrimSpace(parts[0])

	var state SubState
	switch strings.ToLower(stateStr) {
	case "active":
		state = SubStateActive
	case "pending":
		state = SubStatePending
	case "terminated":
		state = SubStateTerminated
	default:
		state = SubStateActive
	}

	var expires int
	var reason string
	for _, param := range parts[1:] {
		param = strings.TrimSpace(param)
		if strings.HasPrefix(param, "expires=") {
			expires, _ = strconv.Atoi(param[8:])
		} else if strings.HasPrefix(param, "reason=") {
			reason = param[7:]
		}
	}

	return state, expires, reason
}

// extractSIPUser extracts the user part from a SIP URI.
// "sip:1001@pbx.local" → "1001", "1001" → "1001"
func extractSIPUser(uri string) string {
	s := uri
	if strings.HasPrefix(s, "sip:") {
		s = s[4:]
	} else if strings.HasPrefix(s, "sips:") {
		s = s[5:]
	}
	if at := strings.Index(s, "@"); at >= 0 {
		return s[:at]
	}
	return s
}
