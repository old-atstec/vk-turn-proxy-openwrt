package proxy

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

const (
	tSecret = "S3CR3T-VALUE-xyz"
	tUser   = "1700000000:turnuser-abc"
)

func TestRedactedJSONHidesTheSecretsAndKeepsTheShape(t *testing.T) {
	resp := map[string]any{
		"response": map[string]any{
			"token":         tSecret,
			"session_key":   tSecret,
			"turn_server":   map[string]any{"username": tUser, "credential": tSecret, "urls": []any{"turn:203.0.113.5:3478?transport=udp"}},
			"anonym_token":  tSecret,
			"error_code":    float64(14),
			"redirect_uri":  "https://id.vk.ru/not_robot_captcha?domain=vk.com&session_token=" + tSecret + "&variant=popup",
			"empty_token":   "",
			"conversation":  "kept",
			"success_token": tSecret,
		},
	}
	s := redactedJSON(resp)
	for _, leak := range []string{tSecret, tUser} {
		if strings.Contains(s, leak) {
			t.Fatalf("a secret reached the log line: %s", s)
		}
	}
	for _, kept := range []string{`"error_code":14`, `"conversation":"kept"`, "203.0.113.5", "not_robot_captcha", `"empty_token":""`} {
		if !strings.Contains(s, kept) {
			t.Errorf("the log line lost %q: %s", kept, s)
		}
	}
}

func TestRedactBodyCutShort(t *testing.T) {
	body := []byte(`{"response":{"status":"OK","success_token":"` + tSecret + `","more":"` + strings.Repeat("x", 400) + `"}}`)
	if s := redactBody(body[:80], 300); strings.Contains(s, tSecret) {
		t.Fatalf("a cut body leaked the token: %s", s)
	}
	if s := redactBody(body, 300); strings.Contains(s, tSecret) || len(s) > 300 {
		t.Fatalf("a whole body leaked the token or ran past the limit: %s", s)
	}
}

func TestRedactURL(t *testing.T) {
	u := "https://id.vk.ru/captcha?session_token=" + tSecret + "&domain=vk.com"
	s := redactURL(u)
	if strings.Contains(s, tSecret) || !strings.Contains(s, "domain=vk.com") {
		t.Fatalf("redactURL = %s", s)
	}
	if redactURL("not a url%%") != "not a url%%" {
		t.Fatal("a string that is not a URL changed")
	}
}

// The dump sites in the package go through the redaction: a scan, since the
// real calls need VK.
func TestTheDiagnosticDumpsAreRedacted(t *testing.T) {
	for file, needles := range map[string][]string{
		"creds.go":          {`log.Printf("vk: captcha required (attempt %d), url: %s", attempt+1, redactURL(captchaImg))`, `log.Printf("vk: step2 response (host=%s, attempt=%d): %s", vkAPIHost(), attempt+1, redactedJSON(resp))`, `log.Printf("vk: step4 (okcdn vchat.joinConversationByLink) response: %s", redactedJSON(resp))`, `log.Printf("vk: captcha error response: %s", redactedJSON(errObj))`},
		"captcha_pow.go":    {`log.Printf("pow: %s response: %s", method, redactBody(body, 300))`},
		"proxy.go":          {`log.Printf("proxy: captcha required, waiting for answer (image: %s)", redactURL(imageURL))`},
		"creds_vkcalls.go":  {`log.Printf("vkcalls: SUCCESS — username=%s, addresses=%v", redacted, addresses)`},
		"creds_vkcookie.go": {`log.Printf("vk-cookie: SUCCESS user=%s addrs=%v", redacted, addrs)`},
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range needles {
			if !bytes.Contains(src, []byte(n)) {
				t.Errorf("%s: no %s", file, n)
			}
		}
	}
	// And nothing marshals a VK response straight into a log line any more.
	for _, file := range []string{"creds.go", "creds_vkcalls.go", "captcha_pow.go"} {
		src, _ := os.ReadFile(file)
		for i, line := range strings.Split(stripComments(string(src)), "\n") {
			if strings.Contains(line, "log.Printf") && strings.Contains(line, "string(respJSON)") {
				t.Errorf("%s:%d logs a raw response: %s", file, i+1, strings.TrimSpace(line))
			}
		}
	}
}
