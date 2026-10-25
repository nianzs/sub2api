package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fishSweepRepoStub 只实现 ListByPlatform，其余方法经嵌入接口保持 panic
//（不会被走到）。
type fishSweepRepoStub struct {
	AccountRepository
	accounts []Account
}

func (r *fishSweepRepoStub) ListByPlatform(_ context.Context, _ string) ([]Account, error) {
	return r.accounts, nil
}

// fishSweepProberStub 记录探测到的账号；failIDs 中的账号返回错误。
type fishSweepProberStub struct {
	mu     sync.Mutex
	probed []int64
	fail   map[int64]struct{}
}

func (p *fishSweepProberStub) GetUsageForAccount(_ context.Context, account *Account, _ ...bool) (*UsageInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probed = append(p.probed, account.ID)
	if _, ok := p.fail[account.ID]; ok {
		return nil, errors.New("probe failed")
	}
	return &UsageInfo{}, nil
}

func (p *fishSweepProberStub) probedIDs() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int64(nil), p.probed...)
}

func fishSweepAccount(id int64, status string, token, baseURL string) Account {
	credentials := map[string]any{}
	if token != "" {
		credentials[fishCredentialToken] = token
	}
	if baseURL != "" {
		credentials[fishCredentialRelayBaseURL] = baseURL
	}
	return Account{
		ID:          id,
		Platform:    PlatformFish,
		Type:        AccountTypeAPIKey,
		Status:      status,
		Credentials: credentials,
	}
}

func TestFishQuotaSweepProbesOnlyActiveTokenAccounts(t *testing.T) {
	repo := &fishSweepRepoStub{accounts: []Account{
		fishSweepAccount(1, StatusActive, "tok", ""),       // token 号：探测
		fishSweepAccount(2, StatusActive, "", "https://r"), // 中转号：跳过
		fishSweepAccount(3, StatusError, "tok", ""),        // 非 active：跳过
		fishSweepAccount(4, StatusActive, "", ""),          // 无 token 非中转：跳过
	}}
	prober := &fishSweepProberStub{}
	svc := NewFishQuotaSweepService(repo, nil, time.Minute)
	svc.prober = prober

	svc.runOnce()

	got := prober.probedIDs()
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("probed = %v, want [1]", got)
	}
}

func TestFishQuotaSweepSingleFailureDoesNotAbortRun(t *testing.T) {
	repo := &fishSweepRepoStub{accounts: []Account{
		fishSweepAccount(1, StatusActive, "tok", ""),
		fishSweepAccount(2, StatusActive, "tok", ""),
		fishSweepAccount(3, StatusActive, "tok", ""),
	}}
	prober := &fishSweepProberStub{fail: map[int64]struct{}{2: {}}}
	svc := NewFishQuotaSweepService(repo, nil, time.Minute)
	svc.prober = prober

	svc.runOnce()

	if got := prober.probedIDs(); len(got) != 3 {
		t.Fatalf("probed = %v, want all 3 despite account 2 failing", got)
	}
}

func TestFishQuotaSweepStartGuards(t *testing.T) {
	// interval<=0、缺 repo、缺 prober：Start/Stop 均为 no-op，不 panic 不挂起。
	repo := &fishSweepRepoStub{}
	svc := NewFishQuotaSweepService(repo, nil, 0)
	svc.Start()
	svc.Stop()

	svc2 := NewFishQuotaSweepService(nil, nil, time.Minute)
	svc2.Start()
	svc2.Stop()
}
