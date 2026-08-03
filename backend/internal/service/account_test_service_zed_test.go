package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The connection test's whole value rests on the /client/users/me readout: a mint
// succeeds even when system_id is wrong, so a swallowed status failure would
// report a guaranteed-broken account as healthy.
func TestEvaluateZedAccountStatus(t *testing.T) {
	for _, tc := range []struct {
		name         string
		info         map[string]any
		err          error
		wantPass     bool
		wantContains string
	}{
		{
			name:         "403 names the likely causes",
			err:          &ZedUpstreamError{StatusCode: 403, Body: "trial_blocked"},
			wantContains: "system_id",
		},
		{
			name:         "401 is a credential rejection and reports its own status",
			err:          &ZedUpstreamError{StatusCode: 401},
			wantContains: "rejected (401)",
		},
		{
			// errors.As, not a type assertion: the mint path wraps upstream errors.
			name:         "wrapped upstream error is still classified as auth failure",
			err:          fmt.Errorf("fetch zed account info: %w", &ZedUpstreamError{StatusCode: 403}),
			wantContains: "system_id",
		},
		{
			name:         "5xx falls to the generic failure branch",
			err:          &ZedUpstreamError{StatusCode: 503},
			wantContains: "does not prove inference will work",
		},
		{
			name:         "transport failure fails the test",
			err:          errors.New("dial tcp: connection refused"),
			wantContains: "connection refused",
		},
		{
			name:         "plan readout passes",
			info:         map[string]any{"plan": "zed_pro"},
			wantPass:     true,
			wantContains: "zed_pro",
		},
		{
			// A field rename upstream must not fail every account: the 200 already
			// proves the credential is in good standing.
			name:         "200 without a plan passes with a caveat",
			info:         map[string]any{},
			wantPass:     true,
			wantContains: "could not be confirmed",
		},
		{
			name:         "nil payload passes with a caveat",
			info:         nil,
			wantPass:     true,
			wantContains: "could not be confirmed",
		},
		{
			name:         "non-string plan passes with a caveat",
			info:         map[string]any{"plan": 123},
			wantPass:     true,
			wantContains: "could not be confirmed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateZedAccountStatus(tc.info, tc.err)
			if got.Pass != tc.wantPass {
				t.Errorf("Pass = %v, want %v (text=%q)", got.Pass, tc.wantPass, got.Text)
			}
			if !strings.Contains(got.Text, tc.wantContains) {
				t.Errorf("Text = %q, want it to mention %q", got.Text, tc.wantContains)
			}
		})
	}
}
