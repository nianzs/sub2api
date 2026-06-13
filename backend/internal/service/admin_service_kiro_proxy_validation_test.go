package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// fakeProxyRepoForValidation is a minimal in-memory ProxyRepository fake
// covering exactly the methods validateKiroProxyBinding/validateKiroBulkProxyRebind call.
type fakeProxyRepoForValidation struct {
	proxies map[int64]*Proxy
	counts  map[int64]int64
	getErr  error
}

func (f *fakeProxyRepoForValidation) GetByID(_ context.Context, id int64) (*Proxy, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	p, ok := f.proxies[id]
	if !ok {
		return nil, ErrProxyNotFound
	}
	return p, nil
}
func (f *fakeProxyRepoForValidation) CountAccountsByProxyID(_ context.Context, id int64) (int64, error) {
	return f.counts[id], nil
}

// Remaining ProxyRepository methods are unused by the validation helpers;
// no-op stubs let the fake satisfy the interface.
func (f *fakeProxyRepoForValidation) Create(context.Context, *Proxy) error { return nil }
func (f *fakeProxyRepoForValidation) ListByIDs(context.Context, []int64) ([]Proxy, error) {
	return nil, nil
}
func (f *fakeProxyRepoForValidation) Update(context.Context, *Proxy) error { return nil }
func (f *fakeProxyRepoForValidation) Delete(context.Context, int64) error  { return nil }
func (f *fakeProxyRepoForValidation) List(context.Context, pagination.PaginationParams) ([]Proxy, *pagination.PaginationResult, error) {
	return nil, nil, nil
}
func (f *fakeProxyRepoForValidation) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string) ([]Proxy, *pagination.PaginationResult, error) {
	return nil, nil, nil
}
func (f *fakeProxyRepoForValidation) ListWithFiltersAndAccountCount(context.Context, pagination.PaginationParams, string, string, string) ([]ProxyWithAccountCount, *pagination.PaginationResult, error) {
	return nil, nil, nil
}
func (f *fakeProxyRepoForValidation) ListActive(context.Context) ([]Proxy, error) { return nil, nil }
func (f *fakeProxyRepoForValidation) ListActiveWithAccountCount(context.Context) ([]ProxyWithAccountCount, error) {
	return nil, nil
}
func (f *fakeProxyRepoForValidation) ExistsByHostPortAuth(context.Context, string, int, string, string) (bool, error) {
	return false, nil
}
func (f *fakeProxyRepoForValidation) ListAccountSummariesByProxyID(context.Context, int64) ([]ProxyAccountSummary, error) {
	return nil, nil
}
func (f *fakeProxyRepoForValidation) SweepExpiredProxies(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (f *fakeProxyRepoForValidation) ListAllForFallback(context.Context) ([]Proxy, error) {
	return nil, nil
}
func (f *fakeProxyRepoForValidation) CountExpired(context.Context) (int64, error) {
	return 0, nil
}
func (f *fakeProxyRepoForValidation) CountExpiringSoon(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func newProxyValidationFake() *fakeProxyRepoForValidation {
	future := time.Now().Add(7 * 24 * time.Hour)
	past := time.Now().Add(-1 * time.Hour)
	return &fakeProxyRepoForValidation{
		proxies: map[int64]*Proxy{
			1: {ID: 1, Status: StatusActive, MaxAccounts: 3, ExpiresAt: &future},
			2: {ID: 2, Status: "inactive", MaxAccounts: 3, ExpiresAt: &future},
			3: {ID: 3, Status: StatusActive, MaxAccounts: 3, ExpiresAt: &past},
			4: {ID: 4, Status: StatusActive, MaxAccounts: 3, ExpiresAt: &future}, // saturated, soft (default)
			5: {ID: 5, Status: StatusActive, MaxAccounts: 3, ExpiresAt: nil},     // never expires
			6: {ID: 6, Status: StatusActive, MaxAccounts: 3, ExpiresAt: &future, EnforceMaxAccounts: true}, // saturated, hard
			7: {ID: 7, Status: StatusActive, MaxAccounts: 3, ExpiresAt: &future, EnforceMaxAccounts: true}, // empty, hard
		},
		counts: map[int64]int64{1: 0, 2: 0, 3: 0, 4: 3, 5: 0, 6: 3, 7: 0},
	}
}

func validationReason(err error) string {
	var pv *ProxyValidationError
	if errors.As(err, &pv) {
		return pv.Reason
	}
	return ""
}

func TestValidateKiroProxyBinding(t *testing.T) {
	t.Parallel()

	repo := newProxyValidationFake()
	s := &adminServiceImpl{proxyRepo: repo}

	pid := func(id int64) *int64 { return &id }

	tests := []struct {
		name          string
		platform      string
		effectiveID   *int64
		checkCapacity bool
		wantErr       bool
		wantReason    string
	}{
		{name: "non-kiro platform always passes (no proxy)", platform: PlatformOpenAI, effectiveID: nil, checkCapacity: true, wantErr: false},
		{name: "non-kiro platform passes (full proxy)", platform: PlatformOpenAI, effectiveID: pid(4), checkCapacity: true, wantErr: false},
		{name: "kiro without proxy allowed (soft, no forced proxy)", platform: PlatformKiro, effectiveID: nil, checkCapacity: true, wantErr: false},
		{name: "kiro zero proxy_id allowed (soft)", platform: PlatformKiro, effectiveID: pid(0), checkCapacity: true, wantErr: false},
		{name: "kiro proxy not found", platform: PlatformKiro, effectiveID: pid(999), checkCapacity: true, wantErr: true, wantReason: ProxyValidationProxyNotFound},
		{name: "kiro proxy inactive", platform: PlatformKiro, effectiveID: pid(2), checkCapacity: true, wantErr: true, wantReason: ProxyValidationProxyNotActive},
		{name: "kiro proxy expired", platform: PlatformKiro, effectiveID: pid(3), checkCapacity: true, wantErr: true, wantReason: ProxyValidationProxyExpired},
		{name: "kiro proxy at capacity soft -> allowed", platform: PlatformKiro, effectiveID: pid(4), checkCapacity: true, wantErr: false},
		{name: "kiro proxy at capacity hard -> CapacityFull", platform: PlatformKiro, effectiveID: pid(6), checkCapacity: true, wantErr: true, wantReason: ProxyValidationCapacityFull},
		{name: "kiro proxy at capacity hard skipped when checkCapacity=false", platform: PlatformKiro, effectiveID: pid(6), checkCapacity: false, wantErr: false},
		{name: "kiro happy path", platform: PlatformKiro, effectiveID: pid(1), checkCapacity: true, wantErr: false},
		{name: "kiro proxy with no expiry passes", platform: PlatformKiro, effectiveID: pid(5), checkCapacity: true, wantErr: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := s.validateKiroProxyBinding(context.Background(), tc.platform, tc.effectiveID, tc.checkCapacity)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
				}
				if got := validationReason(err); got != tc.wantReason {
					t.Fatalf("reason mismatch: got=%q want=%q (err=%v)", got, tc.wantReason, err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateKiroBulkProxyRebind(t *testing.T) {
	t.Parallel()

	pid := func(id int64) *int64 { return &id }
	mkAccount := func(id int64, platform string, proxyID *int64) *Account {
		return &Account{ID: id, Platform: platform, ProxyID: proxyID}
	}

	tests := []struct {
		name       string
		accounts   []*Account
		newProxyID *int64
		wantErr    bool
		wantReason string
	}{
		{name: "nil newProxyID is no-op", accounts: []*Account{mkAccount(1, PlatformKiro, nil)}, newProxyID: nil},
		{name: "no kiro accounts in batch is no-op", accounts: []*Account{mkAccount(1, PlatformOpenAI, nil), mkAccount(2, PlatformOpenAI, pid(1))}, newProxyID: pid(0)},
		{name: "clearing proxy with kiro account allowed (soft)", accounts: []*Account{mkAccount(1, PlatformKiro, pid(1)), mkAccount(2, PlatformOpenAI, nil)}, newProxyID: pid(0), wantErr: false},
		{name: "target proxy not found", accounts: []*Account{mkAccount(1, PlatformKiro, nil)}, newProxyID: pid(999), wantErr: true, wantReason: ProxyValidationProxyNotFound},
		{name: "target proxy inactive", accounts: []*Account{mkAccount(1, PlatformKiro, nil)}, newProxyID: pid(2), wantErr: true, wantReason: ProxyValidationProxyNotActive},
		{name: "target proxy expired", accounts: []*Account{mkAccount(1, PlatformKiro, nil)}, newProxyID: pid(3), wantErr: true, wantReason: ProxyValidationProxyExpired},
		{name: "all kiro already bound to target -> no new bindings -> pass even if proxy looks full", accounts: []*Account{mkAccount(1, PlatformKiro, pid(4)), mkAccount(2, PlatformKiro, pid(4))}, newProxyID: pid(4)},
		{name: "single new binding to proxy with room -> pass", accounts: []*Account{mkAccount(1, PlatformKiro, nil)}, newProxyID: pid(1)},
		{name: "batch of 3 new bindings to empty proxy (capacity 3) -> pass", accounts: []*Account{mkAccount(1, PlatformKiro, nil), mkAccount(2, PlatformKiro, nil), mkAccount(3, PlatformKiro, nil)}, newProxyID: pid(1)},
		{name: "batch of 4 to empty soft proxy (capacity 3) -> allowed", accounts: []*Account{mkAccount(1, PlatformKiro, nil), mkAccount(2, PlatformKiro, nil), mkAccount(3, PlatformKiro, nil), mkAccount(4, PlatformKiro, nil)}, newProxyID: pid(1), wantErr: false},
		{name: "batch of 4 to empty hard proxy (capacity 3) -> CapacityFull", accounts: []*Account{mkAccount(1, PlatformKiro, nil), mkAccount(2, PlatformKiro, nil), mkAccount(3, PlatformKiro, nil), mkAccount(4, PlatformKiro, nil)}, newProxyID: pid(7), wantErr: true, wantReason: ProxyValidationCapacityFull},
		{name: "mix of new + existing on soft proxy 4 -> allowed", accounts: []*Account{mkAccount(1, PlatformKiro, pid(4)), mkAccount(2, PlatformKiro, pid(4)), mkAccount(3, PlatformKiro, nil)}, newProxyID: pid(4), wantErr: false},
		{name: "non-kiro account moved to expired proxy is allowed (helper only checks kiro)", accounts: []*Account{mkAccount(1, PlatformOpenAI, nil)}, newProxyID: pid(3)},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newProxyValidationFake()
			s := &adminServiceImpl{proxyRepo: repo}
			err := s.validateKiroBulkProxyRebind(context.Background(), tc.accounts, tc.newProxyID)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
				}
				if got := validationReason(err); got != tc.wantReason {
					t.Fatalf("reason mismatch: got=%q want=%q (err=%v)", got, tc.wantReason, err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestLockKiroProxyBindSerializes 验证同一 proxy ID 的并发 lock 被串行化，
// 不同 proxy ID 的 lock 互不阻塞。
func TestLockKiroProxyBindSerializes(t *testing.T) {
	t.Parallel()

	s := &adminServiceImpl{}

	var inside atomic.Int32
	var maxInside atomic.Int32

	const goroutines = 8
	done := make(chan struct{}, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			unlock := s.lockKiroProxyBind(42)
			defer unlock()
			cur := inside.Add(1)
			defer inside.Add(-1)
			for {
				old := maxInside.Load()
				if cur <= old || maxInside.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
		}()
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
	if got := maxInside.Load(); got != 1 {
		t.Fatalf("expected at most 1 goroutine inside critical section, observed %d", got)
	}

	unlock1 := s.lockKiroProxyBind(100)
	unlock2 := s.lockKiroProxyBind(200) // 不会死锁
	unlock2()
	unlock1()
}
