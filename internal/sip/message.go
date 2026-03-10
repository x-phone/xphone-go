// Package sip implements a minimal SIP stack for xphone:
// message parsing/building, digest authentication, UDP transport, and client transactions.
package sip

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
)

// Message represents a SIP request or response.
// For requests: Method and RequestURI are set, StatusCode is 0.
// For responses: StatusCode and Reason are set, Method is empty.
type Message struct {
	// Request fields
	Method     string
	RequestURI string

	// Response fields
	StatusCode int
	Reason     string

	// Headers stored as ordered key-value pairs.
	// Keys are stored in their original case; lookups are case-insensitive.
	headers []header

	// Body (e.g. SDP)
	Body []byte
}

type header struct {
	Name  string
	Value string
}

// IsResponse returns true if this message is a SIP response (status line starts with SIP/2.0).
func (m *Message) IsResponse() bool {
	return m.StatusCode > 0
}

// Header returns the first value for the named header (case-insensitive).
// Returns empty string if not found.
func (m *Message) Header(name string) string {
	lower := strings.ToLower(name)
	for _, h := range m.headers {
		if strings.ToLower(h.Name) == lower {
			return h.Value
		}
	}
	return ""
}

// HeaderValues returns all values for the named header (case-insensitive).
func (m *Message) HeaderValues(name string) []string {
	lower := strings.ToLower(name)
	var vals []string
	for _, h := range m.headers {
		if strings.ToLower(h.Name) == lower {
			vals = append(vals, h.Value)
		}
	}
	return vals
}

// SetHeader sets a header, replacing any existing values with the same name.
func (m *Message) SetHeader(name, value string) {
	lower := strings.ToLower(name)
	for i, h := range m.headers {
		if strings.ToLower(h.Name) == lower {
			m.headers[i].Value = value
			// Remove any subsequent duplicates.
			j := i + 1
			for j < len(m.headers) {
				if strings.ToLower(m.headers[j].Name) == lower {
					m.headers = append(m.headers[:j], m.headers[j+1:]...)
				} else {
					j++
				}
			}
			return
		}
	}
	m.headers = append(m.headers, header{Name: name, Value: value})
}

// AddHeader appends a header value (does not replace existing).
func (m *Message) AddHeader(name, value string) {
	m.headers = append(m.headers, header{Name: name, Value: value})
}

// ViaBranch returns the branch parameter from the top Via header.
func (m *Message) ViaBranch() string {
	via := m.Header("Via")
	if via == "" {
		return ""
	}
	return paramValue(via, "branch")
}

// CSeq parses the CSeq header into sequence number and method.
func (m *Message) CSeq() (int, string) {
	val := m.Header("CSeq")
	if val == "" {
		return 0, ""
	}
	parts := strings.SplitN(strings.TrimSpace(val), " ", 2)
	if len(parts) != 2 {
		return 0, ""
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, ""
	}
	return n, parts[1]
}

// FromTag returns the tag parameter from the From header.
func (m *Message) FromTag() string {
	return paramValue(m.Header("From"), "tag")
}

// ToTag returns the tag parameter from the To header.
func (m *Message) ToTag() string {
	return paramValue(m.Header("To"), "tag")
}

// Bytes serializes the message to wire format.
// Content-Length is computed automatically and does not need to be set by the caller.
// This method does not mutate the message.
func (m *Message) Bytes() []byte {
	var buf bytes.Buffer

	// Start line.
	if m.IsResponse() {
		buf.WriteString("SIP/2.0 ")
		buf.WriteString(strconv.Itoa(m.StatusCode))
		buf.WriteString(" ")
		buf.WriteString(m.Reason)
		buf.WriteString("\r\n")
	} else {
		buf.WriteString(m.Method)
		buf.WriteString(" ")
		buf.WriteString(m.RequestURI)
		buf.WriteString(" SIP/2.0\r\n")
	}

	// Headers (skip any caller-set Content-Length — we compute it).
	clKey := "content-length"
	for _, h := range m.headers {
		if strings.ToLower(h.Name) == clKey {
			continue
		}
		buf.WriteString(h.Name)
		buf.WriteString(": ")
		buf.WriteString(h.Value)
		buf.WriteString("\r\n")
	}

	// Write computed Content-Length.
	buf.WriteString("Content-Length: ")
	buf.WriteString(strconv.Itoa(len(m.Body)))
	buf.WriteString("\r\n")

	// Blank line separating headers from body.
	buf.WriteString("\r\n")

	// Body.
	if len(m.Body) > 0 {
		buf.Write(m.Body)
	}

	return buf.Bytes()
}

// parseStartLine parses the first line of a SIP message, populating the
// Message's request or response fields.
func parseStartLine(msg *Message, line string) error {
	if strings.HasPrefix(line, "SIP/2.0 ") {
		rest := line[8:]
		spaceIdx := strings.IndexByte(rest, ' ')
		if spaceIdx < 0 {
			return errors.New("sip: malformed status line")
		}
		code, err := strconv.Atoi(rest[:spaceIdx])
		if err != nil {
			return errors.New("sip: invalid status code")
		}
		msg.StatusCode = code
		msg.Reason = rest[spaceIdx+1:]
		return nil
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 3 || parts[2] != "SIP/2.0" {
		return errors.New("sip: malformed request line")
	}
	msg.Method = parts[0]
	msg.RequestURI = parts[1]
	return nil
}

// extractBody determines the message body from raw data after the header
// section, respecting Content-Length when present.
func extractBody(msg *Message, body []byte) {
	if len(body) == 0 {
		return
	}
	clStr := msg.Header("Content-Length")
	if clStr == "" {
		msg.Body = body
		return
	}
	cl, err := strconv.Atoi(clStr)
	if err != nil || cl < 0 {
		msg.Body = body
		return
	}
	if cl == 0 {
		return
	}
	if cl <= len(body) {
		msg.Body = body[:cl]
	} else {
		msg.Body = body
	}
}

// Parse parses a raw SIP message (request or response).
func Parse(data []byte) (*Message, error) {
	if len(data) == 0 {
		return nil, errors.New("sip: empty message")
	}

	headEnd := bytes.Index(data, []byte("\r\n\r\n"))
	var head, body []byte
	if headEnd < 0 {
		head = data
	} else {
		head = data[:headEnd]
		body = data[headEnd+4:]
	}

	lines := bytes.Split(head, []byte("\r\n"))
	if len(lines) == 0 {
		return nil, errors.New("sip: no start line")
	}

	startLine := string(lines[0])
	if startLine == "" {
		return nil, errors.New("sip: empty start line")
	}

	msg := &Message{}
	if err := parseStartLine(msg, startLine); err != nil {
		return nil, err
	}

	for _, line := range lines[1:] {
		s := string(line)
		if s == "" {
			continue
		}
		colonIdx := strings.IndexByte(s, ':')
		if colonIdx < 0 {
			continue
		}
		msg.headers = append(msg.headers, header{
			Name:  s[:colonIdx],
			Value: strings.TrimSpace(s[colonIdx+1:]),
		})
	}

	if headEnd >= 0 {
		extractBody(msg, body)
	}

	return msg, nil
}

// paramValue extracts a parameter value from a SIP header value string.
// Example: paramValue("SIP/2.0/UDP 10.0.0.1;branch=z9hG4bK123;rport", "branch") => "z9hG4bK123"
func paramValue(headerVal, param string) string {
	search := param + "="
	idx := strings.Index(strings.ToLower(headerVal), strings.ToLower(search))
	if idx < 0 {
		return ""
	}
	start := idx + len(search)
	rest := headerVal[start:]
	// Value ends at semicolon, comma, space, or end of string.
	end := strings.IndexAny(rest, ";, \t>")
	if end < 0 {
		return rest
	}
	return rest[:end]
}
