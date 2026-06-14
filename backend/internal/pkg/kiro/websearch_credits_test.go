package kiro

import (
	"encoding/json"
	"strings"
	"testing"
)

// chunk 构造一个 message_delta SSE chunk，usage 里带 _sub2api_kiro_credits。
func messageDeltaChunk(credits float64) []byte {
	usage := map[string]any{
		"input_tokens":            1,
		"output_tokens":           10,
		"cache_read_input_tokens": 100,
	}
	if credits > 0 {
		usage["_sub2api_kiro_credits"] = credits
	}
	event := map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn"},
		"usage": usage,
	}
	b, _ := json.Marshal(event)
	return []byte("event: message_delta\ndata: " + string(b) + "\n\n")
}

func creditsInChunk(t *testing.T, chunk []byte) (float64, bool) {
	t.Helper()
	for _, line := range strings.Split(string(chunk), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		usage, ok := ev["usage"].(map[string]any)
		if !ok {
			continue
		}
		v, has := usage["_sub2api_kiro_credits"]
		if !has {
			return 0, false
		}
		f, _ := v.(float64)
		return f, true
	}
	return 0, false
}

// TestRewriteFinalKiroCredits_OverwritesTotal 验证最终 message_delta 的 credits
// 被覆写为累计总额。
func TestRewriteFinalKiroCredits_OverwritesTotal(t *testing.T) {
	chunks := [][]byte{
		[]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0}\n\n"),
		messageDeltaChunk(1.5), // 最后一轮本身只带 1.5
	}
	out := RewriteFinalKiroCredits(chunks, 4.2) // 累计总额 4.2
	got, has := creditsInChunk(t, out[1])
	if !has {
		t.Fatal("message_delta 应保留 _sub2api_kiro_credits 字段")
	}
	if got != 4.2 {
		t.Fatalf("credits 应被覆写为累计总额 4.2，实际 %v", got)
	}
}

// TestRewriteFinalKiroCredits_ZeroTotalNoChange 验证 totalCredits<=0 时不改动。
func TestRewriteFinalKiroCredits_ZeroTotalNoChange(t *testing.T) {
	chunks := [][]byte{messageDeltaChunk(1.5)}
	out := RewriteFinalKiroCredits(chunks, 0)
	got, has := creditsInChunk(t, out[0])
	if !has || got != 1.5 {
		t.Fatalf("totalCredits=0 时应保持原值 1.5，实际 has=%v got=%v", has, got)
	}
}

// TestRewriteFinalKiroCredits_InjectsWhenMissing 验证最后一轮无 metering（message_delta
// 不带 _sub2api_kiro_credits）时，累计总额仍被注入，避免漏计费。
func TestRewriteFinalKiroCredits_InjectsWhenMissing(t *testing.T) {
	chunks := [][]byte{messageDeltaChunk(0)} // 最后一轮 0 credits，不带字段
	out := RewriteFinalKiroCredits(chunks, 4.2) // 但前几轮累计 4.2
	got, has := creditsInChunk(t, out[0])
	if !has {
		t.Fatal("缺字段时应注入累计 credits，避免漏计费")
	}
	if got != 4.2 {
		t.Fatalf("应注入累计总额 4.2，实际 %v", got)
	}
}
