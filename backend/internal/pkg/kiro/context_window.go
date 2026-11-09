package kiro

import (
	"math"
	"strings"
)

// KiroContextWindowTokens 返回模型在 Kiro 上游的上下文窗口大小（token），
// 用于把 contextUsageEvent 的使用百分比换算成 token 数。取值与 kiro.rs 的
// get_context_window_size 保持一致。
//
// 换算结果会写进 usage.input_tokens，既影响计费，也影响客户端的上下文进度条
// 与自动压缩阈值。漏配一个 1M 模型会让该模型的 input 少报 5 倍——新增 1M
// 上下文模型时务必同步此表。
func KiroContextWindowTokens(model string) int {
	normalized := normalizeKiroModelKey(model)
	switch normalized {
	case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		return 1_000_000
	case "claude-sonnet-4.6", "claude-sonnet-4.8", "claude-sonnet-5", "claude-sonnet-5.5",
		"claude-opus-4.6", "claude-opus-4.7", "claude-opus-4.8", "claude-opus-5", "claude-opus-5.5",
		"claude-haiku-5.5", "claude-fable-5":
		return 1_000_000
	}
	// 其他 GPT 模型的窗口尚未确认，沿用 kiro.rs 的 272k 兜底。
	if strings.HasPrefix(normalized, "gpt") {
		return 272_000
	}
	return 200_000
}

// normalizeKiroModelKey 把客户端或上游模型名归一成 Kiro 模型 ID（如
// claude-opus-5-5 → claude-opus-5.5），供按模型查表的地方共用。
func normalizeKiroModelKey(model string) string {
	normalized := strings.TrimSpace(strings.ToLower(model))
	// MapModel 对「点号版 + -thinking」（如 claude-opus-4.8-thinking）没有
	// 单独条目，先剪掉后缀再映射。
	normalized = strings.TrimSuffix(normalized, "-thinking")
	if mapped := MapModel(normalized); mapped != "" {
		normalized = mapped
	}
	return normalized
}

// kiroContextTokensFromPercentage 把 contextUsageEvent 的使用百分比换算成 token。
// 窗口按实际上游模型查（requestCtx.UpstreamModelID）：账号模型映射后客户端
// 看到的模型名可能和真实上游模型窗口不同。未设置时回退到 fallbackModel。
func kiroContextTokensFromPercentage(percentage float64, requestCtx KiroRequestContext, fallbackModel string) int {
	model := strings.TrimSpace(requestCtx.UpstreamModelID)
	if model == "" {
		model = fallbackModel
	}
	return int(math.Round(percentage * float64(KiroContextWindowTokens(model)) / 100.0))
}
