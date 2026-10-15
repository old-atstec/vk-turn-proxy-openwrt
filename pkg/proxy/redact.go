package proxy

// Redaction of VK's answers before they reach the log. The diagnostic dumps
// of the credential path (step2, step4, the captcha error, the captcha
// methods) carry tokens, the TURN username and credential and the captcha's
// session token; on a router the log is syslog, readable by anyone with
// logread, and sent to a remote syslog when one is configured. The shape of
// the answer stays readable — keys, lengths, error codes — the values of the
// secret fields do not.

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// isSecretKey: the JSON keys and query parameters whose values never reach
// the log.
func isSecretKey(k string) bool {
	k = strings.ToLower(k)
	switch k {
	case "credential", "password", "username", "secret", "sig", "key", "hash", "session_key", "joinlink":
		return true
	}
	return strings.HasSuffix(k, "token") || strings.HasSuffix(k, "_key") || strings.HasSuffix(k, "secret")
}

const redacted = "<redacted>"

// redactValue returns v with the values of the secret keys replaced, at any
// depth; a string that is a URL has its secret query parameters replaced.
func redactValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			if isSecretKey(k) {
				if s, ok := e.(string); ok && s == "" {
					out[k] = ""
				} else {
					out[k] = redacted
				}
				continue
			}
			out[k] = redactValue(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = redactValue(e)
		}
		return out
	case string:
		if strings.HasPrefix(x, "http://") || strings.HasPrefix(x, "https://") {
			return redactURL(x)
		}
		return x
	}
	return v
}

// redactedJSON is json.Marshal of the redacted value, for a log line.
func redactedJSON(v any) string {
	b, err := json.Marshal(redactValue(v))
	if err != nil {
		return "<unprintable>"
	}
	return string(b)
}

// redactURL replaces the values of a URL's secret query parameters.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return raw
	}
	q := u.Query()
	changed := false
	for k := range q {
		if isSecretKey(k) {
			q[k] = []string{redacted}
			changed = true
		}
	}
	if !changed {
		return raw
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// secretPair finds "key":"value" pairs in text that is not (whole) JSON — a
// body cut at a length limit.
var secretPair = regexp.MustCompile(`"([A-Za-z_]*(?:token|Token|_key|secret|credential|password|username))"\s*:\s*"[^"]*"?`)

// redactBody is a response body for the log, at most limit bytes of it: JSON
// is redacted as a value; anything else, or JSON cut short, by pattern.
func redactBody(body []byte, limit int) string {
	var v any
	if json.Unmarshal(body, &v) == nil {
		s := redactedJSON(v)
		if len(s) > limit {
			s = s[:limit]
		}
		return s
	}
	s := string(body[:min(limit, len(body))])
	return secretPair.ReplaceAllString(s, `"$1":"`+redacted+`"`)
}
