package kiro

import (
	"crypto/sha256"
	"encoding/json"
)

// kiroPromptFingerprintKeep 是保留的边界指纹个数（从最深的往回数）。
const kiroPromptFingerprintKeep = 16

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
// 返回最后 kiroPromptFingerprintKeep 个，按从浅到深排列。
func computeKiroPromptFingerprints(modelID string, tools []KiroToolWrapper, history []KiroHistoryMessage, current *KiroUserInputMessage) [][32]byte {
	h := sha256.New()
	enc := json.NewEncoder(h)
	if err := enc.Encode(kiroFingerprintPrelude{Model: normalizeKiroModelKey(modelID), Tools: tools}); err != nil {
		return nil
	}
	var out [][32]byte
	mark := func() {
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
			mark()
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
		mark()
	}
	if len(out) > kiroPromptFingerprintKeep {
		out = out[len(out)-kiroPromptFingerprintKeep:]
	}
	return out
}
