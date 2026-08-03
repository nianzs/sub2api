package zed

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestUnwrapEvent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "wrapped event is unwrapped",
			in:   `{"event":{"type":"content_block_delta","delta":{"text":"hi"}}}`,
			want: `{"type":"content_block_delta","delta":{"text":"hi"}}`,
		},
		{
			name: "bare event passes through",
			in:   `{"type":"message_stop"}`,
			want: `{"type":"message_stop"}`,
		},
		{
			name: "surrounding whitespace is trimmed",
			in:   "  {\"type\":\"ping\"}\r",
			want: `{"type":"ping"}`,
		},
		{
			name: "non-object event value is not unwrapped",
			in:   `{"event":"stream_ended"}`,
			want: `{"event":"stream_ended"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(UnwrapEvent([]byte(tc.in))); got != tc.want {
				t.Errorf("UnwrapEvent() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestUnwrapEventOnResponsesCompleted(t *testing.T) {
	// The Responses stream carries real usage on response.completed; this is the
	// billing source for GPT models, so unwrapping must expose it.
	line := `{"event":{"type":"response.completed","response":{"id":"resp_1","usage":` +
		`{"input_tokens":11,"output_tokens":22,"total_tokens":33}}}}`

	event := UnwrapEvent([]byte(line))
	if got := EventType(event); got != "response.completed" {
		t.Fatalf("EventType = %q, want response.completed", got)
	}

	var payload struct {
		Response struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(event, &payload); err != nil {
		t.Fatalf("unmarshal completed event: %v", err)
	}
	if payload.Response.Usage.InputTokens != 11 || payload.Response.Usage.OutputTokens != 22 {
		t.Errorf("usage = %+v, want input 11 / output 22", payload.Response.Usage)
	}
}

func TestEventTypeAbsent(t *testing.T) {
	if got := EventType(json.RawMessage(`{"choices":[]}`)); got != "" {
		t.Errorf("EventType = %q, want empty for chat-style chunks", got)
	}
}

func TestParseJWTExpiry(t *testing.T) {
	// {"exp":1893456000} base64url without padding.
	token := "header.eyJleHAiOjE4OTM0NTYwMDB9.signature"

	exp, err := ParseJWTExpiry(token)
	if err != nil {
		t.Fatalf("ParseJWTExpiry: %v", err)
	}
	if want := time.Unix(1893456000, 0); !exp.Equal(want) {
		t.Errorf("exp = %v, want %v", exp, want)
	}
}

func TestParseJWTExpiryRejectsMalformed(t *testing.T) {
	for _, token := range []string{
		"",
		"onlyonesegment",
		"header.!!!notbase64!!!.sig",
		"header.eyJzdWIiOiJhYmMifQ.sig", // no exp claim
	} {
		if _, err := ParseJWTExpiry(token); err == nil {
			t.Errorf("ParseJWTExpiry(%q) should have failed", token)
		}
	}
}

func TestMintAuthorization(t *testing.T) {
	// The upstream expects "<user_id> <credential json>", not a bearer scheme.
	got := MintAuthorization("1234", `{"access_token":"x"}`)
	if want := `1234 {"access_token":"x"}`; got != want {
		t.Errorf("MintAuthorization = %q, want %q", got, want)
	}
}

func TestApplyCompletionHeadersSetsAntiAbuseSet(t *testing.T) {
	// Every one of these is validated upstream; omitting any makes a working
	// account fail with trial_blocked (403).
	h := http.Header{}
	ApplyCompletionHeaders(h, "jwt-value", "sys-1", "1.13.1")

	want := map[string]string{
		"Authorization":              "Bearer jwt-value",
		"Content-Type":               "application/json",
		HeaderZedVersion:             "1.13.1",
		HeaderZedSystemID:            "sys-1",
		HeaderSupportsStatusMessages: "true",
		HeaderSupportsStreamEnded:    "true",
	}
	for name, wantValue := range want {
		if got := h.Get(name); got != wantValue {
			t.Errorf("header %s = %q, want %q", name, got, wantValue)
		}
	}
	if ua := h.Get("User-Agent"); ua != UserAgent("1.13.1") {
		t.Errorf("User-Agent = %q, want the Zed-shaped UA", ua)
	}
}

func TestApplyCompletionHeadersDefaultsVersion(t *testing.T) {
	h := http.Header{}
	ApplyCompletionHeaders(h, "jwt", "", "")
	if got := h.Get(HeaderZedVersion); got != DefaultZedVersion {
		t.Errorf("x-zed-version = %q, want %q", got, DefaultZedVersion)
	}
	if h.Get(HeaderZedSystemID) != "" {
		t.Error("x-zed-system-id must be omitted when unknown rather than sent empty")
	}
}

func TestApplyMintHeaders(t *testing.T) {
	h := http.Header{}
	ApplyMintHeaders(h, "1234", `{"access_token":"x"}`, "sys-1", "1.13.1")

	if got := h.Get("Authorization"); got != `1234 {"access_token":"x"}` {
		t.Errorf("Authorization = %q, want the user_id + credential form", got)
	}
	if got := h.Get(HeaderZedSystemID); got != "sys-1" {
		t.Errorf("x-zed-system-id = %q, want sys-1", got)
	}
}
