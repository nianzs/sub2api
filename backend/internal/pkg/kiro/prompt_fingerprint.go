package kiro

import (
	"crypto/sha256"
	"encoding/json"
)

// kiroPromptCacheLookbackMessages 是 Kiro 提示词缓存往回找的消息条数：新请求在被缓存的
// 提示词之后最多追加这么多条消息仍能命中。2026-11-09 实测：追加 20 条命中，22 条不命中；
// 一条消息里有多个工具调用或工具结果也只算一条。
const kiroPromptCacheLookbackMessages = 20

type kiroFingerprintPrelude struct {
	Model string            `json:"m"`
	Tools []KiroToolWrapper `json:"t,omitempty"`
}

type kiroFingerprintUser struct {
	Role        string           `json:"r"`
	Content     string           `json:"c"`
	Images      []KiroImage      `json:"i,omitempty"`
	ToolResults []KiroToolResult `json:"tr,omitempty"`
}

type kiroFingerprintAssistant struct {
	Role     string        `json:"r"`
	Content  string        `json:"c"`
	ToolUses []KiroToolUse `json:"tu,omitempty"`
}

// computeKiroPromptFingerprints 按 Kiro 实际收到的内容计算提示词前缀指纹，
// 每条 user 消息之后取一个，最后一个就是完整提示词（含当前消息）。
//
// Kiro 的提示词缓存只在新提示词完整延续之前某个请求的完整提示词时命中。
// 请求 N 的当前消息在请求 N+1 里会变成 history，所以指纹只哈希消息内容、
// 图片和工具结果/调用，不哈希只挂在当前消息上的 tools；tools 与模型 ID
// 放进前置段，变化时所有指纹都变（Kiro 的缓存同样会失效）。
// 只返回之后不超过 kiroPromptCacheLookbackMessages 条消息的边界（更早的前缀 Kiro
// 不会命中），按从浅到深排列；条数按最终发给 Kiro 的 history 加当前消息计。
func computeKiroPromptFingerprints(modelID string, tools []KiroToolWrapper, history []KiroHistoryMessage, current *KiroUserInputMessage) [][32]byte {
	h := sha256.New()
	enc := json.NewEncoder(h)
	if err := enc.Encode(kiroFingerprintPrelude{Model: normalizeKiroModelKey(modelID), Tools: tools}); err != nil {
		return nil
	}
	total := len(history)
	if current != nil {
		total++
	}
	var out [][32]byte
	// mark 在第 pos 条消息（history 下标，当前消息为 len(history)）之后取指纹。
	mark := func(pos int) {
		if total-1-pos > kiroPromptCacheLookbackMessages {
			return
		}
		var fp [32]byte
		copy(fp[:], h.Sum(nil))
		out = append(out, fp)
	}
	encodeUser := func(m *KiroUserInputMessage) bool {
		rec := kiroFingerprintUser{Role: "u", Content: m.Content, Images: m.Images}
		if m.UserInputMessageContext != nil && len(m.UserInputMessageContext.ToolResults) > 0 {
			rec.ToolResults = m.UserInputMessageContext.ToolResults
		}
		return enc.Encode(rec) == nil
	}
	for i := range history {
		switch {
		case history[i].UserInputMessage != nil:
			if !encodeUser(history[i].UserInputMessage) {
				return nil
			}
			mark(i)
		case history[i].AssistantResponseMessage != nil:
			a := history[i].AssistantResponseMessage
			if enc.Encode(kiroFingerprintAssistant{Role: "a", Content: a.Content, ToolUses: a.ToolUses}) != nil {
				return nil
			}
		}
	}
	if current != nil {
		if !encodeUser(current) {
			return nil
		}
		mark(len(history))
	}
	return out
}
