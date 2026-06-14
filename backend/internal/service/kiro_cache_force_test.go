//go:build unit

package service

import "testing"

// TestApplyKiroCacheForceRatio_ReshapesDistribution 验证强制重塑把分布变成
// Anthropic-like：input 极小、cache_read 占大头，且总量守恒。
func TestApplyKiroCacheForceRatio_ReshapesDistribution(t *testing.T) {
	u := &kiroCacheEmulationUsage{InputTokens: 50000, CacheReadInputTokens: 0, CacheCreationInputTokens: 0}
	applyKiroCacheForceRatio(u)

	total := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	if total != 50000 {
		t.Fatalf("总量必须守恒，got %d", total)
	}
	if u.InputTokens >= 100 {
		t.Fatalf("input 应被压到极小，got %d", u.InputTokens)
	}
	if u.CacheReadInputTokens <= u.CacheCreationInputTokens {
		t.Fatalf("cache_read 应占大头：cr=%d cc=%d", u.CacheReadInputTokens, u.CacheCreationInputTokens)
	}
	if u.CacheCreation5mInputTokens != u.CacheCreationInputTokens {
		t.Fatalf("cache_creation 应归到 5m TTL")
	}
}

// TestApplyKiroCacheForceRatio_ShortRequest 验证极短请求只兜底 input=0->1。
func TestApplyKiroCacheForceRatio_ShortRequest(t *testing.T) {
	u := &kiroCacheEmulationUsage{InputTokens: 0, CacheReadInputTokens: 30}
	applyKiroCacheForceRatio(u)
	if u.InputTokens != 1 || u.CacheReadInputTokens != 30 {
		t.Fatalf("极短请求应只兜底 input=1，got input=%d cr=%d", u.InputTokens, u.CacheReadInputTokens)
	}
}

// TestEffectiveKiroCacheForceRatioCenter 验证取值/兜底/cap。
func TestEffectiveKiroCacheForceRatioCenter(t *testing.T) {
	if v := (&Group{Platform: PlatformKiro, KiroCacheForceRatioCenter: 0.925}).EffectiveKiroCacheForceRatioCenter(); v != 0.925 {
		t.Fatalf("want 0.925, got %v", v)
	}
	if v := (&Group{Platform: PlatformAnthropic, KiroCacheForceRatioCenter: 0.925}).EffectiveKiroCacheForceRatioCenter(); v != 0 {
		t.Fatalf("非 kiro 平台应返回 0, got %v", v)
	}
	if v := (&Group{Platform: PlatformKiro, KiroCacheForceRatioCenter: 2}).EffectiveKiroCacheForceRatioCenter(); v != kiroCacheForceRatioCenterMaxCap {
		t.Fatalf("超 cap 应被钳到 %v, got %v", kiroCacheForceRatioCenterMaxCap, v)
	}
}
