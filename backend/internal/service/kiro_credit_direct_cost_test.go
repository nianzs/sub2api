//go:build unit

package service

import (
	"math"
	"testing"
)

func approxEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestApplyKiroCreditDirectCost_FinalPriceIgnoresMultiplier 验证 Kiro credit 直接计费：
// 无论 token 成本经过多少倍率，最终 ActualCost == credits × target_usd。
func TestApplyKiroCreditDirectCost_FinalPriceIgnoresMultiplier(t *testing.T) {
	// 模拟一个已被 rate multiplier(1.5) 改写过的 cost：token 成本本来 0.10，×1.5 = 0.15。
	cost := &CostBreakdown{
		InputCost:     0.02,
		OutputCost:    0.05,
		CacheReadCost: 0.03,
		TotalCost:     0.10,
		ActualCost:    0.15, // 已叠加 1.5 倍率
	}
	credits := 1.25
	target := 0.04 // USD/credit

	applyKiroCreditDirectCost(cost, credits, target)

	want := credits * target // 0.05
	if !approxEqual(cost.ActualCost, want) {
		t.Fatalf("ActualCost 应严格等于 credits×target=%.4f（不叠加倍率），实际 %.4f", want, cost.ActualCost)
	}
	if !approxEqual(cost.TotalCost, want) {
		t.Fatalf("TotalCost 应等于 %.4f，实际 %.4f", want, cost.TotalCost)
	}
	// 子项之和应保持等于 TotalCost 的不变式。
	sum := cost.InputCost + cost.OutputCost + cost.ImageOutputCost + cost.CacheCreationCost + cost.CacheReadCost
	if !approxEqual(sum, cost.TotalCost) {
		t.Fatalf("子项之和(%.6f)应等于 TotalCost(%.6f)", sum, cost.TotalCost)
	}
}

// TestApplyKiroCreditDirectCost_ZeroTotalFallback 验证 token 成本为 0 时兜底：
// 全部归到 input 子项，ActualCost 仍 == credits×target。
func TestApplyKiroCreditDirectCost_ZeroTotalFallback(t *testing.T) {
	cost := &CostBreakdown{TotalCost: 0, ActualCost: 0}
	applyKiroCreditDirectCost(cost, 2.0, 0.04)
	want := 2.0 * 0.04
	if !approxEqual(cost.ActualCost, want) || !approxEqual(cost.InputCost, want) {
		t.Fatalf("TotalCost=0 兜底失败：ActualCost=%.4f InputCost=%.4f want=%.4f", cost.ActualCost, cost.InputCost, want)
	}
}

// TestApplyKiroCreditDirectCost_NoopOnZeroInputs 验证 credits/target<=0 或 nil 时不改动。
func TestApplyKiroCreditDirectCost_NoopOnZeroInputs(t *testing.T) {
	cost := &CostBreakdown{TotalCost: 0.10, ActualCost: 0.15}
	applyKiroCreditDirectCost(cost, 0, 0.04)
	if !approxEqual(cost.ActualCost, 0.15) {
		t.Fatalf("credits=0 时不应改动 ActualCost，实际 %.4f", cost.ActualCost)
	}
	applyKiroCreditDirectCost(cost, 1.0, 0)
	if !approxEqual(cost.ActualCost, 0.15) {
		t.Fatalf("target=0 时不应改动 ActualCost，实际 %.4f", cost.ActualCost)
	}
	applyKiroCreditDirectCost(nil, 1.0, 0.04) // 不应 panic
}

// TestApplyKiroCreditDirectCost_SanityCapClamps 验证畸高 credits 被 clamp 到上限，
// 防止上游异常一次扣巨额。
func TestApplyKiroCreditDirectCost_SanityCapClamps(t *testing.T) {
	cost := &CostBreakdown{TotalCost: 0.10, ActualCost: 0.10}
	applyKiroCreditDirectCost(cost, 100000, 1.0) // 畸高 credits
	want := kiroCreditsPerRequestSanityCap * 1.0
	if !approxEqual(cost.ActualCost, want) {
		t.Fatalf("credits 应被 clamp 到 cap，ActualCost want=%.2f got=%.2f", want, cost.ActualCost)
	}
}
