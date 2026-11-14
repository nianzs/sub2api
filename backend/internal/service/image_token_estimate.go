package service

import (
	"encoding/json"
	"math"
	"strings"
)

// gptImageQualityTokens1024 是各质量档 1024x1024 出图的输出 token 数（gpt-image-1
// 官方表）。xhigh / max 没有公开数据，按 high 计。上游不报 usage 的出图渠道
// （fish 原生号的估算）用这张表。
var gptImageQualityTokens1024 = map[string]float64{
	"low":    272,
	"medium": 1056,
	"high":   4160,
	"xhigh":  4160,
	"max":    4160,
}

// gptImageQualityTokens 返回质量档在 1024x1024 下的输出 token 数（大小写不敏感）；
// 未知档位返回 0。
func gptImageQualityTokens(quality string) float64 {
	return gptImageQualityTokens1024[strings.ToLower(strings.TrimSpace(quality))]
}

// estimateGPTImageOutputTokens 按交付像素估算单张图的输出 token：
// 1024x1024 表值 × (width × height ÷ 1024²)。在 gpt-image-1 公布的尺寸上与官方
// 数字一致（如 1024x1536 medium = 1584）；gpt-image-2 实际按内部画布切分计费，
// 只能作为估算。未知档位或非法尺寸返回 0。
func estimateGPTImageOutputTokens(quality string, width, height int) int {
	base := gptImageQualityTokens(quality)
	if base <= 0 || width <= 0 || height <= 0 {
		return 0
	}
	return int(math.Round(base * float64(width) * float64(height) / (1024 * 1024)))
}

// openAIImageUsageJSON 构造 OpenAI Images 形状的 usage：提示词按模型对应的分词器
// 计数（gpt-image 为 o200k）；参考图输入 token 没有公开公式，记 0。
// outputTokens ≤ 0 时返回 nil（响应不带 usage）。
func openAIImageUsageJSON(model, prompt string, outputTokens int) json.RawMessage {
	if outputTokens <= 0 {
		return nil
	}
	textTokens := 0
	if codec, err := openAIInputTokensCodecForModel(model); err == nil {
		if n, err := codec.Count(prompt); err == nil {
			textTokens = n
		}
	}
	usage, err := json.Marshal(map[string]any{
		"input_tokens":          textTokens,
		"input_tokens_details":  map[string]int{"text_tokens": textTokens, "image_tokens": 0},
		"output_tokens":         outputTokens,
		"output_tokens_details": map[string]int{"image_tokens": outputTokens, "text_tokens": 0},
		"total_tokens":          textTokens + outputTokens,
	})
	if err != nil {
		return nil
	}
	return usage
}
