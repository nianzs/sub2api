package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// Codex 压缩请求在 Kiro 上超限后的一次性补救，与 kiro.rs responses_compaction.rs 一致：
// 只在上游确认超限之后、只对压缩请求，把所有工具输出压到总预算内再重试一次。
const (
	kiroCompactionToolOutputBudgetBytes = 25_000
	kiroCompactionToolOutputMinBytes    = 1_024
	// kiroCompactionOverflowMessage 是重试后仍超限时返回给 Codex 的文案（与 kiro.rs 一致）。
	kiroCompactionOverflowMessage = "upstream context window exceeded after compaction recovery"
)

// errKiroCompactionNoRetry 表示不能再重试：已经重试过，或者没有可截的工具输出。
var errKiroCompactionNoRetry = errors.New("kiro compaction: no retry available")

type kiroCompactionTruncationStats struct {
	Items         int
	RemovedBytes  int
	OriginalBytes int
	RetainedBytes int
}

// newKiroCompactionRetry 返回一次性的重试函数：把 body 里的工具输出压到预算内后用
// send 重发。第二次调用、或者没有可截的工具输出时返回 errKiroCompactionNoRetry。
func newKiroCompactionRetry(body []byte, model string, send func([]byte) (*http.Response, error)) func() (*http.Response, error) {
	used := false
	return func() (*http.Response, error) {
		if used {
			return nil, errKiroCompactionNoRetry
		}
		used = true
		bounded, stats := boundAnthropicToolResultsForCompactionRetry(body)
		if stats.Items == 0 {
			logger.L().Warn("kiro compaction exceeded the context window and had no oversized tool outputs to reduce",
				zap.String("model", model))
			return nil, errKiroCompactionNoRetry
		}
		logger.L().Warn("kiro compaction exceeded the context window; retrying with bounded tool outputs",
			zap.String("model", model),
			zap.Int("truncated_tool_outputs", stats.Items),
			zap.Int("removed_bytes", stats.RemovedBytes),
			zap.Int("original_tool_output_bytes", stats.OriginalBytes),
			zap.Int("retained_tool_output_bytes", stats.RetainedBytes),
		)
		return send(bounded)
	}
}

// retryKiroCompactionOnContextLength 在压缩请求被 Kiro 以「输入太长」400 拒绝时重试一次。
// 不是这类错误、或者无法重试时，原样返回 resp（body 已复位，后续照常处理）。
func retryKiroCompactionOnContextLength(resp *http.Response, retry func() (*http.Response, error)) (*http.Response, error) {
	if resp == nil || resp.StatusCode != http.StatusBadRequest || retry == nil {
		return resp, nil
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if _, ok := kiroContextLengthClientMessage(resp.StatusCode, body); !ok {
		return resp, nil
	}
	next, err := retry()
	if errors.Is(err, errKiroCompactionNoRetry) {
		return resp, nil
	}
	if err != nil {
		return nil, err
	}
	return next, nil
}

// boundAnthropicToolResultsForCompactionRetry 把 Anthropic 请求体里所有 tool_result 的文本
// 压到总预算 kiroCompactionToolOutputBudgetBytes 内，算法与 kiro.rs bound_tool_outputs_for_retry
// 一致：每条至少保留 min(1KB, 预算/条数)，超出部分从最旧的开始削，最新的结果尽量保持完整。
// tool_use_id 和块结构不变，工具配对不受影响；图片块不动。
func boundAnthropicToolResultsForCompactionRetry(body []byte) ([]byte, kiroCompactionTruncationStats) {
	type textUnit struct {
		path string
		text string
	}
	var units []textUnit
	gjson.GetBytes(body, "messages").ForEach(func(mi, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(bi, block gjson.Result) bool {
			if block.Get("type").String() != "tool_result" {
				return true
			}
			base := fmt.Sprintf("messages.%d.content.%d.content", mi.Int(), bi.Int())
			content := block.Get("content")
			switch {
			case content.Type == gjson.String:
				units = append(units, textUnit{path: base, text: content.String()})
			case content.IsArray():
				content.ForEach(func(ci, part gjson.Result) bool {
					if part.Get("type").String() == "text" {
						units = append(units, textUnit{path: fmt.Sprintf("%s.%d.text", base, ci.Int()), text: part.Get("text").String()})
					}
					return true
				})
			}
			return true
		})
		return true
	})

	var stats kiroCompactionTruncationStats
	for _, unit := range units {
		stats.OriginalBytes += len(unit.text)
	}
	stats.RetainedBytes = stats.OriginalBytes
	if len(units) == 0 || stats.OriginalBytes <= kiroCompactionToolOutputBudgetBytes {
		return body, stats
	}

	floor := min(kiroCompactionToolOutputMinBytes, kiroCompactionToolOutputBudgetBytes/len(units))
	excess := stats.OriginalBytes - kiroCompactionToolOutputBudgetBytes
	out := body
	for _, unit := range units {
		if excess <= 0 {
			break
		}
		removable := len(unit.text) - min(floor, len(unit.text))
		removed := min(excess, removable)
		if removed <= 0 {
			continue
		}
		truncated := truncateMiddleForKiroCompaction(unit.text, len(unit.text)-removed)
		actual := len(unit.text) - len(truncated)
		if actual <= 0 {
			continue
		}
		next, err := sjson.SetBytes(out, unit.path, truncated)
		if err != nil {
			continue
		}
		out = next
		stats.Items++
		stats.RemovedBytes += actual
		stats.RetainedBytes -= actual
		excess -= actual
	}
	return out, stats
}

// truncateMiddleForKiroCompaction 把 text 截到不超过 maxBytes：保留前 3/4、后 1/4，
// 中间插入说明（含原始大小），按 UTF-8 边界切。与 kiro.rs truncate_middle 一致。
func truncateMiddleForKiroCompaction(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	marker := fmt.Sprintf("\n[... tool output truncated for compaction retry; original size: %d bytes ...]\n", len(text))
	if len(marker) >= maxBytes {
		// 说明本身是 ASCII，直接按字节截断。
		return marker[:max(maxBytes, 0)]
	}
	contentBudget := maxBytes - len(marker)
	headEnd := min(contentBudget*3/4, len(text))
	for headEnd > 0 && !utf8.RuneStart(text[headEnd]) {
		headEnd--
	}
	tailStart := len(text) - (contentBudget - contentBudget*3/4)
	for tailStart < len(text) && !utf8.RuneStart(text[tailStart]) {
		tailStart++
	}
	return text[:headEnd] + marker + text[tailStart:]
}

// addClaudeUsage 累加多次上游尝试的用量（压缩重试时两次都计费）。
func addClaudeUsage(a, b ClaudeUsage) ClaudeUsage {
	a.InputTokens += b.InputTokens
	a.OutputTokens += b.OutputTokens
	a.CacheCreationInputTokens += b.CacheCreationInputTokens
	a.CacheReadInputTokens += b.CacheReadInputTokens
	a.CacheCreation5mTokens += b.CacheCreation5mTokens
	a.CacheCreation1hTokens += b.CacheCreation1hTokens
	a.ImageOutputTokens += b.ImageOutputTokens
	a.KiroCredits += b.KiroCredits
	return a
}
