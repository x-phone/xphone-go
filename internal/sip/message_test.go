package sip

import (
	"strconv"
	"testing"
)

// --- Parsing SIP Responses ---

func TestParseResponse_200OK(t *testing.T) {
	raw := "SIP/2.0 200 OK\r\n" +
		"Via: SIP/2.0/UDP 192.168.1.100:5060;branch=z9hG4bK776asdhds\r\n" +
		"From: <sip:1001@pbx.example.com>;tag=1928301774\r\n" +
		"To: <sip:1001@pbx.example.com>;tag=a6c85cf\r\n" +
		"Call-ID: a84b4c76e66710@192.168.1.100\r\n" +
		"CSeq: 314159 REGISTER\r\n" +
		"Contact: <sip:1001@192.168.1.100:5060>\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if !msg.IsResponse() {
		t.Fatal("expected response, got request")
	}
	if msg.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", msg.StatusCode)
	}
	if msg.Reason != "OK" {
		t.Errorf("Reason = %q, want %q", msg.Reason, "OK")
	}
	if got := msg.Header("Call-ID"); got != "a84b4c76e66710@192.168.1.100" {
		t.Errorf("Call-ID = %q, want %q", got, "a84b4c76e66710@192.168.1.100")
	}
	if got := msg.Header("CSeq"); got != "314159 REGISTER" {
		t.Errorf("CSeq = %q, want %q", got, "314159 REGISTER")
	}
}

func TestParseResponse_401Challenge(t *testing.T) {
	raw := "SIP/2.0 401 Unauthorized\r\n" +
		"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK1234\r\n" +
		"From: <sip:1001@pbx.local>;tag=abc\r\n" +
		"To: <sip:1001@pbx.local>;tag=def\r\n" +
		"Call-ID: call123@10.0.0.1\r\n" +
		"CSeq: 1 REGISTER\r\n" +
		`WWW-Authenticate: Digest realm="asterisk",nonce="abc123def",algorithm=MD5` + "\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if msg.StatusCode != 401 {
		t.Errorf("StatusCode = %d, want 401", msg.StatusCode)
	}
	if msg.Reason != "Unauthorized" {
		t.Errorf("Reason = %q, want %q", msg.Reason, "Unauthorized")
	}
	auth := msg.Header("WWW-Authenticate")
	if auth == "" {
		t.Fatal("WWW-Authenticate header missing")
	}
	if got := msg.Header("www-authenticate"); got != auth {
		t.Error("header lookup should be case-insensitive")
	}
}

func TestParseResponse_180Ringing(t *testing.T) {
	raw := "SIP/2.0 180 Ringing\r\n" +
		"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK5678\r\n" +
		"From: <sip:1001@pbx.local>;tag=aaa\r\n" +
		"To: <sip:1002@pbx.local>;tag=bbb\r\n" +
		"Call-ID: inv001@10.0.0.1\r\n" +
		"CSeq: 1 INVITE\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if msg.StatusCode != 180 {
		t.Errorf("StatusCode = %d, want 180", msg.StatusCode)
	}
	if msg.Reason != "Ringing" {
		t.Errorf("Reason = %q, want %q", msg.Reason, "Ringing")
	}
}

func TestParseResponse_MultiWordReason(t *testing.T) {
	raw := "SIP/2.0 486 Busy Here\r\n" +
		"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bKxyz\r\n" +
		"Call-ID: call@host\r\n" +
		"CSeq: 1 INVITE\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if msg.StatusCode != 486 {
		t.Errorf("StatusCode = %d, want 486", msg.StatusCode)
	}
	if msg.Reason != "Busy Here" {
		t.Errorf("Reason = %q, want %q", msg.Reason, "Busy Here")
	}
}

// --- Parsing SIP Requests ---

func TestParseRequest_INVITE(t *testing.T) {
	sdpBody := "v=0\r\no=- 0 0 IN IP4 10.0.0.1\r\ns=-\r\nc=IN IP4 10.0.0.1\r\nt=0 0\r\nm=audio 10000 RTP/AVP 0\r\n"
	raw := "INVITE sip:1002@pbx.local SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bKinv1\r\n" +
		"From: <sip:1001@pbx.local>;tag=from1\r\n" +
		"To: <sip:1002@pbx.local>\r\n" +
		"Call-ID: invite001@10.0.0.1\r\n" +
		"CSeq: 1 INVITE\r\n" +
		"Contact: <sip:1001@10.0.0.1:5060>\r\n" +
		"Content-Type: application/sdp\r\n" +
		"Content-Length: " + strconv.Itoa(len(sdpBody)) + "\r\n" +
		"\r\n" +
		sdpBody

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if msg.IsResponse() {
		t.Fatal("expected request, got response")
	}
	if msg.Method != "INVITE" {
		t.Errorf("Method = %q, want %q", msg.Method, "INVITE")
	}
	if msg.RequestURI != "sip:1002@pbx.local" {
		t.Errorf("RequestURI = %q, want %q", msg.RequestURI, "sip:1002@pbx.local")
	}
	if string(msg.Body) != sdpBody {
		t.Errorf("Body = %q, want %q", string(msg.Body), sdpBody)
	}
}

func TestParseRequest_REGISTER(t *testing.T) {
	raw := "REGISTER sip:pbx.local SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bKreg1\r\n" +
		"From: <sip:1001@pbx.local>;tag=reg1\r\n" +
		"To: <sip:1001@pbx.local>\r\n" +
		"Call-ID: reg001@10.0.0.1\r\n" +
		"CSeq: 1 REGISTER\r\n" +
		"Contact: <sip:1001@10.0.0.1:5060>\r\n" +
		"Expires: 3600\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if msg.Method != "REGISTER" {
		t.Errorf("Method = %q, want %q", msg.Method, "REGISTER")
	}
	if msg.RequestURI != "sip:pbx.local" {
		t.Errorf("RequestURI = %q, want %q", msg.RequestURI, "sip:pbx.local")
	}
	if got := msg.Header("Expires"); got != "3600" {
		t.Errorf("Expires = %q, want %q", got, "3600")
	}
}

func TestParseRequest_BYE(t *testing.T) {
	raw := "BYE sip:1001@10.0.0.1:5060 SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP pbx.local:5060;branch=z9hG4bKbye1\r\n" +
		"From: <sip:1002@pbx.local>;tag=from2\r\n" +
		"To: <sip:1001@pbx.local>;tag=to2\r\n" +
		"Call-ID: invite001@10.0.0.1\r\n" +
		"CSeq: 2 BYE\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if msg.Method != "BYE" {
		t.Errorf("Method = %q, want %q", msg.Method, "BYE")
	}
}

// --- Header Access ---

func TestHeader_CaseInsensitive(t *testing.T) {
	raw := "SIP/2.0 200 OK\r\n" +
		"call-id: lower@host\r\n" +
		"CSeq: 1 REGISTER\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	// Should find regardless of case used in lookup.
	for _, name := range []string{"Call-ID", "call-id", "CALL-ID", "Call-Id"} {
		if got := msg.Header(name); got != "lower@host" {
			t.Errorf("Header(%q) = %q, want %q", name, got, "lower@host")
		}
	}
}

func TestHeader_Missing(t *testing.T) {
	raw := "SIP/2.0 200 OK\r\n" +
		"Call-ID: x@y\r\n" +
		"CSeq: 1 REGISTER\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if got := msg.Header("X-Nonexistent"); got != "" {
		t.Errorf("Header(X-Nonexistent) = %q, want empty", got)
	}
}

func TestHeaderValues_MultipleValues(t *testing.T) {
	raw := "SIP/2.0 200 OK\r\n" +
		"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK111\r\n" +
		"Via: SIP/2.0/UDP 10.0.0.2:5060;branch=z9hG4bK222\r\n" +
		"Call-ID: multi@host\r\n" +
		"CSeq: 1 INVITE\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	vias := msg.HeaderValues("Via")
	if len(vias) != 2 {
		t.Fatalf("Via count = %d, want 2", len(vias))
	}
	if vias[0] != "SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK111" {
		t.Errorf("Via[0] = %q", vias[0])
	}
	if vias[1] != "SIP/2.0/UDP 10.0.0.2:5060;branch=z9hG4bK222" {
		t.Errorf("Via[1] = %q", vias[1])
	}
}

// --- Building SIP Requests ---

func TestBuildRequest_REGISTER(t *testing.T) {
	msg := &Message{
		Method:     "REGISTER",
		RequestURI: "sip:pbx.local",
	}
	msg.SetHeader("Via", "SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bKtest1")
	msg.SetHeader("From", "<sip:1001@pbx.local>;tag=t1")
	msg.SetHeader("To", "<sip:1001@pbx.local>")
	msg.SetHeader("Call-ID", "build-test@10.0.0.1")
	msg.SetHeader("CSeq", "1 REGISTER")
	msg.SetHeader("Contact", "<sip:1001@10.0.0.1:5060>")

	data := msg.Bytes()
	got := string(data)

	// Should start with request line.
	if want := "REGISTER sip:pbx.local SIP/2.0\r\n"; got[:len(want)] != want {
		t.Errorf("request line = %q, want %q", got[:len(want)], want)
	}

	// Round-trip: parse it back.
	parsed, err := Parse(data)
	if err != nil {
		t.Fatalf("round-trip Parse() error: %v", err)
	}
	if parsed.Method != "REGISTER" {
		t.Errorf("round-trip Method = %q, want REGISTER", parsed.Method)
	}
	if parsed.Header("Call-ID") != "build-test@10.0.0.1" {
		t.Errorf("round-trip Call-ID = %q", parsed.Header("Call-ID"))
	}
}

func TestBuildRequest_INVITEWithBody(t *testing.T) {
	sdpBody := "v=0\r\no=- 0 0 IN IP4 10.0.0.1\r\ns=-\r\n"
	msg := &Message{
		Method:     "INVITE",
		RequestURI: "sip:1002@pbx.local",
		Body:       []byte(sdpBody),
	}
	msg.SetHeader("Via", "SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bKinv1")
	msg.SetHeader("From", "<sip:1001@pbx.local>;tag=f1")
	msg.SetHeader("To", "<sip:1002@pbx.local>")
	msg.SetHeader("Call-ID", "inv-build@10.0.0.1")
	msg.SetHeader("CSeq", "1 INVITE")
	msg.SetHeader("Content-Type", "application/sdp")

	data := msg.Bytes()

	// Round-trip.
	parsed, err := Parse(data)
	if err != nil {
		t.Fatalf("round-trip Parse() error: %v", err)
	}
	if parsed.Method != "INVITE" {
		t.Errorf("Method = %q, want INVITE", parsed.Method)
	}
	if string(parsed.Body) != sdpBody {
		t.Errorf("Body = %q, want %q", string(parsed.Body), sdpBody)
	}
	// Content-Length should be set automatically.
	if cl := parsed.Header("Content-Length"); cl != strconv.Itoa(len(sdpBody)) {
		t.Errorf("Content-Length = %q, want %q", cl, strconv.Itoa(len(sdpBody)))
	}
}

// --- Building SIP Responses ---

func TestBuildResponse(t *testing.T) {
	msg := &Message{
		StatusCode: 200,
		Reason:     "OK",
	}
	msg.SetHeader("Via", "SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bKtest1")
	msg.SetHeader("From", "<sip:1001@pbx.local>;tag=t1")
	msg.SetHeader("To", "<sip:1001@pbx.local>;tag=t2")
	msg.SetHeader("Call-ID", "resp-test@10.0.0.1")
	msg.SetHeader("CSeq", "1 REGISTER")

	data := msg.Bytes()
	got := string(data)

	if want := "SIP/2.0 200 OK\r\n"; got[:len(want)] != want {
		t.Errorf("status line = %q, want %q", got[:len(want)], want)
	}

	// Round-trip.
	parsed, err := Parse(data)
	if err != nil {
		t.Fatalf("round-trip Parse() error: %v", err)
	}
	if parsed.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", parsed.StatusCode)
	}
	if parsed.Reason != "OK" {
		t.Errorf("Reason = %q, want OK", parsed.Reason)
	}
}

// --- Via Branch ---

func TestViaBranch(t *testing.T) {
	raw := "SIP/2.0 200 OK\r\n" +
		"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bKmybranch;rport\r\n" +
		"Call-ID: via@host\r\n" +
		"CSeq: 1 REGISTER\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if got := msg.ViaBranch(); got != "z9hG4bKmybranch" {
		t.Errorf("ViaBranch() = %q, want %q", got, "z9hG4bKmybranch")
	}
}

func TestViaBranch_Missing(t *testing.T) {
	raw := "SIP/2.0 200 OK\r\n" +
		"Via: SIP/2.0/UDP 10.0.0.1:5060\r\n" +
		"Call-ID: via2@host\r\n" +
		"CSeq: 1 REGISTER\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if got := msg.ViaBranch(); got != "" {
		t.Errorf("ViaBranch() = %q, want empty", got)
	}
}

// --- CSeq Parsing ---

func TestCSeqMethod(t *testing.T) {
	raw := "SIP/2.0 200 OK\r\n" +
		"Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bKx\r\n" +
		"Call-ID: cseq@host\r\n" +
		"CSeq: 42 INVITE\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	seq, method := msg.CSeq()
	if seq != 42 {
		t.Errorf("CSeq number = %d, want 42", seq)
	}
	if method != "INVITE" {
		t.Errorf("CSeq method = %q, want INVITE", method)
	}
}

// --- From/To Tag ---

func TestFromTag(t *testing.T) {
	raw := "SIP/2.0 200 OK\r\n" +
		"From: <sip:1001@pbx.local>;tag=fromtag123\r\n" +
		"To: <sip:1002@pbx.local>;tag=totag456\r\n" +
		"Call-ID: tag@host\r\n" +
		"CSeq: 1 INVITE\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if got := msg.FromTag(); got != "fromtag123" {
		t.Errorf("FromTag() = %q, want %q", got, "fromtag123")
	}
	if got := msg.ToTag(); got != "totag456" {
		t.Errorf("ToTag() = %q, want %q", got, "totag456")
	}
}

// --- Error Cases ---

func TestParse_Empty(t *testing.T) {
	_, err := Parse([]byte(""))
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestParse_GarbageInput(t *testing.T) {
	_, err := Parse([]byte("this is not a SIP message"))
	if err == nil {
		t.Fatal("expected error for garbage input")
	}
}

func TestParse_TruncatedStatusLine(t *testing.T) {
	_, err := Parse([]byte("SIP/2.0\r\n\r\n"))
	if err == nil {
		t.Fatal("expected error for truncated status line")
	}
}

func TestParse_InvalidStatusCode(t *testing.T) {
	_, err := Parse([]byte("SIP/2.0 abc OK\r\n\r\n"))
	if err == nil {
		t.Fatal("expected error for non-numeric status code")
	}
}

func TestParse_NoHeaders(t *testing.T) {
	// A response with just a status line and empty body should still parse.
	raw := "SIP/2.0 200 OK\r\n" +
		"\r\n"

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if msg.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", msg.StatusCode)
	}
}

// --- Body Handling ---

func TestParse_BodyByContentLength(t *testing.T) {
	body := "v=0\r\no=test\r\n"
	raw := "INVITE sip:1002@pbx.local SIP/2.0\r\n" +
		"Content-Length: " + strconv.Itoa(len(body)) + "\r\n" +
		"Content-Type: application/sdp\r\n" +
		"\r\n" +
		body

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if string(msg.Body) != body {
		t.Errorf("Body = %q, want %q", string(msg.Body), body)
	}
}

func TestParse_NoBody(t *testing.T) {
	raw := "BYE sip:1001@10.0.0.1 SIP/2.0\r\n" +
		"Content-Length: 0\r\n" +
		"\r\n"

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if len(msg.Body) != 0 {
		t.Errorf("Body length = %d, want 0", len(msg.Body))
	}
}

// --- AddHeader (append, not replace) ---

func TestAddHeader(t *testing.T) {
	msg := &Message{StatusCode: 200, Reason: "OK"}
	msg.AddHeader("Via", "SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK111")
	msg.AddHeader("Via", "SIP/2.0/UDP 10.0.0.2:5060;branch=z9hG4bK222")

	vias := msg.HeaderValues("Via")
	if len(vias) != 2 {
		t.Fatalf("Via count = %d, want 2", len(vias))
	}
}

// --- Bytes auto-sets Content-Length ---

func TestBytes_AutoContentLength(t *testing.T) {
	msg := &Message{
		Method:     "INVITE",
		RequestURI: "sip:1002@pbx.local",
		Body:       []byte("testbody"),
	}
	msg.SetHeader("Call-ID", "auto-cl@host")
	msg.SetHeader("CSeq", "1 INVITE")

	parsed, err := Parse(msg.Bytes())
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if parsed.Header("Content-Length") != "8" {
		t.Errorf("Content-Length = %q, want %q", parsed.Header("Content-Length"), "8")
	}
}

func TestBytes_ZeroContentLength(t *testing.T) {
	msg := &Message{
		Method:     "BYE",
		RequestURI: "sip:1001@10.0.0.1",
	}
	msg.SetHeader("Call-ID", "bye-cl@host")
	msg.SetHeader("CSeq", "2 BYE")

	parsed, err := Parse(msg.Bytes())
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if parsed.Header("Content-Length") != "0" {
		t.Errorf("Content-Length = %q, want %q", parsed.Header("Content-Length"), "0")
	}
}
