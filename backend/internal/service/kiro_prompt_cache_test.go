//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type fakeKiroPromptCacheRemote struct {
	mu        sync.Mutex
	entries   map[kiroPromptCacheKey]KiroPromptCacheEntry
	lookupErr error
}

func newFakeKiroPromptCacheRemote() *fakeKiroPromptCacheRemote {
	return &fakeKiroPromptCacheRemote{entries: map[kiroPromptCacheKey]KiroPromptCacheEntry{}}
}

func (f *fakeKiroPromptCacheRemote) Lookup(_ context.Context, accountID int64, fps [][32]byte) (map[[32]byte]KiroPromptCacheEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	out := map[[32]byte]KiroPromptCacheEntry{}
	for _, fp := range fps {
		if e, ok := f.entries[kiroPromptCacheKey{accountID, fp}]; ok {
			out[fp] = e
		}
	}
	return out, nil
}

func (f *fakeKiroPromptCacheRemote) Store(_ context.Context, accountID int64, fp [32]byte, e KiroPromptCacheEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[kiroPromptCacheKey{accountID, fp}] = e
	return nil
}

func syncKiroPromptCacheDispatch(t *testing.T) {
	t.Helper()
	prev := kiroPromptCacheAsyncDispatch
	kiroPromptCacheAsyncDispatch = func(fn func()) { fn() }
	t.Cleanup(func() { kiroPromptCacheAsyncDispatch = prev })
}

func kiroTestFP(seed byte) [32]byte {
	var out [32]byte
	out[0] = seed
	return out
}

func TestKiroUsageToClaudeDoesNotDoubleCountCachedContext(t *testing.T) {
	// 缓存模拟把整段上下文划进 cache_creation，input 为 0：不应再补本地估算。
	usage := kiroUsageToClaude(kiropkg.Usage{CacheCreationInputTokens: 45044}, 26458)
	require.Equal(t, 0, usage.InputTokens)
	require.Equal(t, 45044, usage.CacheCreationInputTokens)

	// 完全没有输入侧数据时仍然兜底。
	usage = kiroUsageToClaude(kiropkg.Usage{OutputTokens: 3}, 26458)
	require.Equal(t, 26458, usage.InputTokens)
}

func TestSelectKiroPromptCacheMatch(t *testing.T) {
	now := time.Now()
	fps := [][32]byte{kiroTestFP(1), kiroTestFP(2), kiroTestFP(3)}
	entries := map[[32]byte]KiroPromptCacheEntry{
		kiroTestFP(1): {PromptTokens: 1000, StartedAt: now.Add(-time.Minute)},
		kiroTestFP(2): {PromptTokens: 2000, StartedAt: now.Add(-2 * time.Minute)},
	}
	require.Equal(t, 2000, selectKiroPromptCacheMatch(fps, entries, now), "取最深的有效匹配")

	entries[kiroTestFP(2)] = KiroPromptCacheEntry{PromptTokens: 2000, StartedAt: now.Add(-6 * time.Minute)}
	require.Equal(t, 1000, selectKiroPromptCacheMatch(fps, entries, now), "超过 5 分钟的记录已过期")

	entries[kiroTestFP(1)] = KiroPromptCacheEntry{PromptTokens: 1000, StartedAt: now.Add(time.Second)}
	require.Equal(t, 0, selectKiroPromptCacheMatch(fps, entries, now), "晚于本请求发出的记录不算")
}

func TestResolveKiroPromptCacheRemoteDownWithoutLocalIsUnresolved(t *testing.T) {
	remote := newFakeKiroPromptCacheRemote()
	remote.lookupErr = errors.New("redis down")
	svc := &GatewayService{kiroPromptCacheRemote: remote}
	ctx := kiropkg.KiroRequestContext{PromptFingerprints: [][32]byte{kiroTestFP(9)}, UpstreamSentAt: time.Now()}
	svc.resolveKiroPromptCache(context.Background(), &Account{ID: 880001}, &ctx)
	require.False(t, ctx.PromptCache.Resolved, "远端不可用且本地没有记录时不能当成未命中")
}

func TestResolveKiroPromptCacheWithoutFingerprintsStaysUnresolved(t *testing.T) {
	svc := &GatewayService{kiroPromptCacheRemote: newFakeKiroPromptCacheRemote()}
	ctx := kiropkg.KiroRequestContext{UpstreamSentAt: time.Now()}
	svc.resolveKiroPromptCache(context.Background(), &Account{ID: 880002}, &ctx)
	require.False(t, ctx.PromptCache.Resolved)
}

func TestRecordKiroPromptCacheSkipsUntrustedTotals(t *testing.T) {
	syncKiroPromptCacheDispatch(t)
	remote := newFakeKiroPromptCacheRemote()
	svc := &GatewayService{kiroPromptCacheRemote: remote}
	ctx := kiropkg.KiroRequestContext{PromptFingerprints: [][32]byte{kiroTestFP(5)}, UpstreamSentAt: time.Now()}

	svc.recordKiroPromptCache(&Account{ID: 880003}, ctx, kiropkg.KiroOutputEstimate{ContextTokens: 1000, Reason: "context_full"})
	require.Empty(t, remote.entries)

	// 没用上 credits 时用"总量 - 本地可见输出"近似，保持会话里的命中链不断。
	svc.recordKiroPromptCache(&Account{ID: 880003}, ctx, kiropkg.KiroOutputEstimate{ContextTokens: 1000, VisibleOutputTokens: 100, Reason: "out_of_bounds"})
	require.Equal(t, 900, remote.entries[kiroPromptCacheKey{880003, kiroTestFP(5)}].PromptTokens)
}

func kiroChainBody(t *testing.T, extra ...map[string]any) []byte {
	t.Helper()
	msgs := []map[string]any{
		{"role": "user", "content": "read main.go"},
		{"role": "assistant", "content": []map[string]any{
			{"type": "tool_use", "id": "toolu_1", "name": "read", "input": map[string]any{"path": "main.go"}},
		}},
		{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "toolu_1", "content": "package main"}}},
	}
	msgs = append(msgs, extra...)
	b, err := json.Marshal(map[string]any{
		"model": "claude-opus-5-5", "system": "You are a coding agent.", "messages": msgs,
		"tools": []map[string]any{{"name": "read", "description": "read a file", "input_schema": map[string]any{"type": "object"}}},
	})
	require.NoError(t, err)
	return b
}

// 两轮连续请求：第一轮完成后记录提示词大小，第二轮发出时推断出的命中量就是它。
func TestKiroPromptCacheChainAcrossTurns(t *testing.T) {
	syncKiroPromptCacheDispatch(t)
	remote := newFakeKiroPromptCacheRemote()
	svc := &GatewayService{kiroPromptCacheRemote: remote}
	account := &Account{ID: 880004}

	turn1, err := kiropkg.BuildKiroPayloadWithContext(kiroChainBody(t), "claude-opus-5.5", "", "AI_EDITOR", nil)
	require.NoError(t, err)
	ctx1 := turn1.Context
	ctx1.UpstreamSentAt = time.Now().Add(-30 * time.Second)
	svc.resolveKiroPromptCache(context.Background(), account, &ctx1)
	require.True(t, ctx1.PromptCache.Resolved)
	require.Equal(t, 0, ctx1.PromptCache.CachedTokens, "第一轮没有可命中的记录")
	svc.recordKiroPromptCache(account, ctx1, kiropkg.KiroOutputEstimate{
		Method: kiropkg.KiroOutputMethodCredits, ContextTokens: 100000, PromptTokens: 97000,
	})

	turn2, err := kiropkg.BuildKiroPayloadWithContext(kiroChainBody(t,
		map[string]any{"role": "assistant", "content": "It is an empty main package."},
		map[string]any{"role": "user", "content": "add a hello world"},
	), "claude-opus-5.5", "", "AI_EDITOR", nil)
	require.NoError(t, err)
	ctx2 := turn2.Context
	ctx2.UpstreamSentAt = time.Now()
	// 换一个"实例"：只靠远端存储也能查到。
	globalKiroPromptCacheMemory.mu.Lock()
	delete(globalKiroPromptCacheMemory.entries, kiroPromptCacheKey{account.ID, ctx1.PromptFingerprints[len(ctx1.PromptFingerprints)-1]})
	globalKiroPromptCacheMemory.mu.Unlock()
	svc.resolveKiroPromptCache(context.Background(), account, &ctx2)
	require.True(t, ctx2.PromptCache.Resolved)
	require.Equal(t, 97000, ctx2.PromptCache.CachedTokens)
}

func TestKiroPromptCacheMemoryPruneIsThrottledAndCapped(t *testing.T) {
	prevAt, prevCap, prevInterval := kiroPromptCacheMemoryPruneAt, kiroPromptCacheMemoryHardCap, kiroPromptCacheMemoryPruneInterval
	t.Cleanup(func() {
		kiroPromptCacheMemoryPruneAt, kiroPromptCacheMemoryHardCap, kiroPromptCacheMemoryPruneInterval = prevAt, prevCap, prevInterval
	})
	kiroPromptCacheMemoryPruneAt, kiroPromptCacheMemoryHardCap, kiroPromptCacheMemoryPruneInterval = 2, 100, time.Hour

	m := &kiroPromptCacheMemory{entries: map[kiroPromptCacheKey]KiroPromptCacheEntry{}}
	old := KiroPromptCacheEntry{PromptTokens: 1, StartedAt: time.Now().Add(-time.Hour)}
	fresh := KiroPromptCacheEntry{PromptTokens: 1, StartedAt: time.Now()}
	m.store(1, kiroTestFP(1), old)
	m.store(1, kiroTestFP(2), old)
	m.store(1, kiroTestFP(3), fresh) // 达到阈值：清掉两条过期的
	require.Len(t, m.entries, 1)

	m.store(1, kiroTestFP(4), old)
	m.store(1, kiroTestFP(5), fresh) // 一小时内已清理过：不再全表扫描
	require.Len(t, m.entries, 3, "清理应按间隔节流")

	kiroPromptCacheMemoryHardCap = 3
	m.store(1, kiroTestFP(6), fresh) // 超过硬上限：整表清空后只剩这一条
	require.Len(t, m.entries, 1)
}

func TestLogKiroOutputEstimateFlagsOverriddenCacheHint(t *testing.T) {
	account := &Account{ID: 7}
	hinted := kiropkg.KiroRequestContext{
		PromptFingerprints: [][32]byte{kiroTestFP(1)},
		PromptCache:        kiropkg.KiroPromptCacheHint{Resolved: true, CachedTokens: 112000},
	}
	credited := func(assumption string) kiropkg.KiroOutputEstimate {
		return kiropkg.KiroOutputEstimate{Method: kiropkg.KiroOutputMethodCredits, CacheAssumption: assumption, CachedPromptTokens: 100000}
	}
	cases := []struct {
		name       string
		requestCtx kiropkg.KiroRequestContext
		est        kiropkg.KiroOutputEstimate
		wantMsg    string
		wantLevel  zapcore.Level
	}{
		{"整段命中兜底", hinted, credited(kiropkg.KiroCacheAssumptionAllCached), "kiro.output_cache_hint_overridden", zap.InfoLevel},
		{"全部未命中兜底", hinted, credited(kiropkg.KiroCacheAssumptionNone), "kiro.output_cache_hint_overridden", zap.InfoLevel},
		{"采用推断值", hinted, credited(kiropkg.KiroCacheAssumptionHint), "", zap.DebugLevel},
		{"没有推断值", kiropkg.KiroRequestContext{PromptFingerprints: hinted.PromptFingerprints, PromptCache: kiropkg.KiroPromptCacheHint{Resolved: true}}, credited(kiropkg.KiroCacheAssumptionNone), "", zap.DebugLevel},
		{"反推被否决", hinted, kiropkg.KiroOutputEstimate{Reason: "out_of_bounds"}, "kiro.output_from_credits_rejected", zap.WarnLevel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			ctx := logger.IntoContext(context.Background(), zap.New(core))
			logKiroOutputEstimate(ctx, account, "claude-opus-5.5", tc.requestCtx, tc.est)
			if tc.wantMsg == "" {
				require.Zero(t, logs.Len(), "只应打 debug 日志")
				return
			}
			require.Equal(t, 1, logs.Len())
			entry := logs.All()[0]
			require.Equal(t, tc.wantMsg, entry.Message)
			require.Equal(t, tc.wantLevel, entry.Level)
			require.Equal(t, tc.est.CacheAssumption, entry.ContextMap()["cache_assumption"])
			require.EqualValues(t, 112000, entry.ContextMap()["hinted_cached_tokens"])
		})
	}
}
