package kiro

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

func fpBody(t *testing.T, model string, tools []map[string]any, messages []map[string]any) []byte {
	t.Helper()
	body := map[string]any{"model": model, "system": "You are a coding agent.", "messages": messages}
	if tools != nil {
		body["tools"] = tools
	}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	return b
}

func buildFingerprints(t *testing.T, kiroModel string, body []byte) [][32]byte {
	t.Helper()
	res, err := BuildKiroPayloadWithContext(body, kiroModel, "", "AI_EDITOR", nil)
	require.NoError(t, err)
	return res.Context.PromptFingerprints
}

func containsFingerprint(fps [][32]byte, want [32]byte) bool {
	for _, fp := range fps {
		if fp == want {
			return true
		}
	}
	return false
}

var fpTools = []map[string]any{{
	"name": "read", "description": "read a file",
	"input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
}}

func turnN() []map[string]any {
	return []map[string]any{
		{"role": "user", "content": []map[string]any{
			{"type": "text", "text": "look at this screenshot and read main.go"},
			{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": tinyPNG}},
		}},
		{"role": "assistant", "content": []map[string]any{
			{"type": "text", "text": "Reading it."},
			{"type": "tool_use", "id": "toolu_1", "name": "read", "input": map[string]any{"path": "main.go"}},
		}},
		{"role": "user", "content": []map[string]any{
			{"type": "tool_result", "tool_use_id": "toolu_1", "content": "package main"},
		}},
	}
}

func TestPromptFingerprintFullPromptMatchesNextTurnBoundary(t *testing.T) {
	n := buildFingerprints(t, "claude-opus-5.5", fpBody(t, "claude-opus-5-5", fpTools, turnN()))
	require.NotEmpty(t, n)

	next := append(turnN(),
		map[string]any{"role": "assistant", "content": "It is an empty main package."},
		map[string]any{"role": "user", "content": "now add a hello world"},
	)
	n1 := buildFingerprints(t, "claude-opus-5.5", fpBody(t, "claude-opus-5-5", fpTools, next))
	require.True(t, containsFingerprint(n1, n[len(n)-1]), "上一轮完整提示词应是下一轮的某个前缀")
	require.NotEqual(t, n[len(n)-1], n1[len(n1)-1])
}

func TestPromptFingerprintExactRepeatMatches(t *testing.T) {
	a := buildFingerprints(t, "claude-opus-5.5", fpBody(t, "claude-opus-5-5", fpTools, turnN()))
	b := buildFingerprints(t, "claude-opus-5.5", fpBody(t, "claude-opus-5-5", fpTools, turnN()))
	require.Equal(t, a, b)
}

func TestPromptFingerprintChangesWithToolsOrModel(t *testing.T) {
	base := buildFingerprints(t, "claude-opus-5.5", fpBody(t, "claude-opus-5-5", fpTools, turnN()))

	otherTools := []map[string]any{fpTools[0], {
		"name": "grep", "description": "search",
		"input_schema": map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}},
	}}
	withTool := buildFingerprints(t, "claude-opus-5.5", fpBody(t, "claude-opus-5-5", otherTools, turnN()))
	otherModel := buildFingerprints(t, "claude-opus-5", fpBody(t, "claude-opus-5", fpTools, turnN()))
	for _, fp := range base {
		require.False(t, containsFingerprint(withTool, fp), "工具变化后不应有共同前缀")
		require.False(t, containsFingerprint(otherModel, fp), "模型变化后不应有共同前缀")
	}
}

func TestPromptFingerprintOnlyForModelsWithRates(t *testing.T) {
	require.Nil(t, buildFingerprints(t, "claude-haiku-4.5", fpBody(t, "claude-haiku-4-5-20251001", fpTools, turnN())))
	t.Setenv(kiroCreditsOutputEnvKey, "off")
	require.Nil(t, buildFingerprints(t, "claude-opus-5.5", fpBody(t, "claude-opus-5-5", fpTools, turnN())))
}

// 复刻 2026-11-09 的探针：在只有一条 user 消息的请求之后追加 20 条消息仍命中，追加 22 条不命中。
func TestPromptFingerprintLookbackLimit(t *testing.T) {
	first := []map[string]any{{"role": "user", "content": "big prompt"}}
	n := buildFingerprints(t, "claude-opus-5.5", fpBody(t, "claude-opus-5-5", nil, first))
	require.NotEmpty(t, n)
	full := n[len(n)-1]

	appendRounds := func(rounds int) [][32]byte {
		msgs := append([]map[string]any{}, first...)
		for i := 0; i < rounds; i++ {
			msgs = append(msgs,
				map[string]any{"role": "assistant", "content": "OK"},
				map[string]any{"role": "user", "content": fmt.Sprintf("reply %d", i)},
			)
		}
		return buildFingerprints(t, "claude-opus-5.5", fpBody(t, "claude-opus-5-5", nil, msgs))
	}
	require.True(t, containsFingerprint(appendRounds(10), full), "追加 20 条消息应仍能匹配")
	require.False(t, containsFingerprint(appendRounds(11), full), "追加 22 条消息不应再匹配")
}

func TestPromptFingerprintBoundaryCount(t *testing.T) {
	var msgs []map[string]any
	for i := 0; i < 30; i++ {
		msgs = append(msgs, map[string]any{"role": "user", "content": "q"}, map[string]any{"role": "assistant", "content": "a"})
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": "last"})
	fps := buildFingerprints(t, "claude-opus-5.5", fpBody(t, "claude-opus-5-5", nil, msgs))
	// 之后 0、2、…、20 条消息的 user 边界，共 11 个。
	require.Len(t, fps, kiroPromptCacheLookbackMessages/2+1)

	full := buildFingerprints(t, "claude-opus-5.5", fpBody(t, "claude-opus-5-5", nil, append(msgs,
		map[string]any{"role": "assistant", "content": "a"},
		map[string]any{"role": "user", "content": "next"},
	)))
	require.True(t, containsFingerprint(full, fps[len(fps)-1]), "最后一个指纹应是完整提示词")
}
