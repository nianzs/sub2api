package service

import (
	"net/url"
	"testing"
)

func TestProxyURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		proxy Proxy
		want  string
	}{
		{
			name: "without auth",
			proxy: Proxy{
				Protocol: "http",
				Host:     "proxy.example.com",
				Port:     8080,
			},
			want: "http://proxy.example.com:8080",
		},
		{
			name: "with auth",
			proxy: Proxy{
				Protocol: "socks5",
				Host:     "socks.example.com",
				Port:     1080,
				Username: "user",
				Password: "pass",
			},
			want: "socks5://user:pass@socks.example.com:1080",
		},
		{
			name: "username only keeps no auth for compatibility",
			proxy: Proxy{
				Protocol: "http",
				Host:     "proxy.example.com",
				Port:     8080,
				Username: "user-only",
			},
			want: "http://proxy.example.com:8080",
		},
		{
			name: "with special characters in credentials",
			proxy: Proxy{
				Protocol: "http",
				Host:     "proxy.example.com",
				Port:     3128,
				Username: "first last@corp",
				Password: "p@ ss:#word",
			},
			want: "http://first%20last%40corp:p%40%20ss%3A%23word@proxy.example.com:3128",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.proxy.URL(); got != tc.want {
				t.Fatalf("Proxy.URL() mismatch: got=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestProxyURL_SpecialCharactersRoundTrip(t *testing.T) {
	t.Parallel()

	proxy := Proxy{
		Protocol: "http",
		Host:     "proxy.example.com",
		Port:     3128,
		Username: "first last@corp",
		Password: "p@ ss:#word",
	}

	parsed, err := url.Parse(proxy.URL())
	if err != nil {
		t.Fatalf("parse proxy URL failed: %v", err)
	}
	if got := parsed.User.Username(); got != proxy.Username {
		t.Fatalf("username mismatch after parse: got=%q want=%q", got, proxy.Username)
	}
	pass, ok := parsed.User.Password()
	if !ok {
		t.Fatal("password missing after parse")
	}
	if pass != proxy.Password {
		t.Fatalf("password mismatch after parse: got=%q want=%q", pass, proxy.Password)
	}
}

func TestProxyHasCapacity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		max     int
		current int64
		want    bool
	}{
		{name: "under capacity", max: 3, current: 2, want: true},
		{name: "at capacity", max: 3, current: 3, want: false},
		{name: "over capacity", max: 3, current: 5, want: false},
		{name: "zero max treated as unlimited (legacy)", max: 0, current: 100, want: true},
		{name: "negative max treated as unlimited (legacy)", max: -1, current: 100, want: true},
		{name: "max=1 single seat available", max: 1, current: 0, want: true},
		{name: "max=1 single seat taken", max: 1, current: 1, want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &Proxy{MaxAccounts: tc.max}
			if got := p.HasCapacity(tc.current); got != tc.want {
				t.Fatalf("HasCapacity(%d) with max=%d: got=%v want=%v",
					tc.current, tc.max, got, tc.want)
			}
		})
	}

	var nilProxy *Proxy
	if nilProxy.HasCapacity(0) {
		t.Fatal("nil Proxy.HasCapacity() should be false")
	}
}

func TestProxyValidationErrorMessage(t *testing.T) {
	t.Parallel()

	withMsg := &ProxyValidationError{Reason: "x", Message: "human readable"}
	if got := withMsg.Error(); got == "" {
		t.Fatal("Error() should not be empty when Message is set")
	}

	bareErr := &ProxyValidationError{Reason: ProxyValidationCapacityFull}
	if got := bareErr.Error(); got == "" {
		t.Fatal("Error() should not be empty when only Reason is set")
	}
}

func TestProxyIDPtrEqual(t *testing.T) {
	t.Parallel()

	a := int64(1)
	b := int64(1)
	c := int64(2)

	if !proxyIDPtrEqual(nil, nil) {
		t.Fatal("nil == nil should be true")
	}
	if proxyIDPtrEqual(&a, nil) {
		t.Fatal("non-nil vs nil should be false")
	}
	if proxyIDPtrEqual(nil, &b) {
		t.Fatal("nil vs non-nil should be false")
	}
	if !proxyIDPtrEqual(&a, &b) {
		t.Fatal("&1 == &1 should be true")
	}
	if proxyIDPtrEqual(&a, &c) {
		t.Fatal("&1 == &2 should be false")
	}
}
