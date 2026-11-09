package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// withKiroCacheScopeClient 临时把网关设置缓存里的分桶开关置为 enabled。
func withKiroCacheScopeClient(t *testing.T, enabled bool) *GatewayService {
	t.Helper()
	prev := gatewayForwardingCache.Load()
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{
		fingerprintUnification: true,
		kiroCacheScopeClient:   enabled,
		expiresAt:              time.Now().Add(time.Hour).UnixNano(),
	})
	t.Cleanup(func() {
		if prev != nil {
			gatewayForwardingCache.Store(prev)
		} else {
			gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
		}
	})
	return &GatewayService{settingService: &SettingService{}}
}

func kiroCacheBodyWithUserID(t *testing.T, label, userID string) []byte {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal(kiroCacheRequestBody(label, false), &payload))
	if userID != "" {
		payload["metadata"] = map[string]any{"user_id": userID}
	}
	out, err := json.Marshal(payload)
	require.NoError(t, err)
	return out
}

func kiroCacheKeyCtx(apiKeyID int64) context.Context {
	return context.WithValue(context.Background(), ctxkey.APIKeyID, apiKeyID)
}

func TestKiroCacheScopeClientDisabledKeepsAccountIsolation(t *testing.T) {
	resetKiroCacheTracker()
	svc := withKiroCacheScopeClient(t, false)
	group := kiroCacheGroup(1)
	body := kiroCacheBodyWithUserID(t, "scope off", "user_x_account__session_aaaa-1111")
	ctx := kiroCacheKeyCtx(9)
	_ = svc.buildKiroCacheEmulationUsage(ctx, kiroCacheAccount(1, "r", "a"), group, body, "claude-sonnet-4-6", 2000)
	other := svc.buildKiroCacheEmulationUsage(ctx, kiroCacheAccount(2, "r", "a"), group, body, "claude-sonnet-4-6", 2000)
	require.NotNil(t, other)
	require.Zero(t, other.CacheReadInputTokens, "开关关闭时换账号不应命中")
}

func TestKiroCacheScopeClientSurvivesAccountSwitch(t *testing.T) {
	resetKiroCacheTracker()
	svc := withKiroCacheScopeClient(t, true)
	group := kiroCacheGroup(1)
	body := kiroCacheBodyWithUserID(t, "scope on", "user_x_account__session_aaaa-1111")
	ctx := kiroCacheKeyCtx(9)
	_ = svc.buildKiroCacheEmulationUsage(ctx, kiroCacheAccount(1, "r", "a"), group, body, "claude-sonnet-4-6", 2000)
	other := svc.buildKiroCacheEmulationUsage(ctx, kiroCacheAccount(2, "r", "a"), group, body, "claude-sonnet-4-6", 2000)
	require.NotNil(t, other)
	require.Equal(t, 2000, other.CacheReadInputTokens, "同 key + session 换账号应命中")
}

func TestKiroCacheScopeClientIsolatesSessionsAndKeys(t *testing.T) {
	resetKiroCacheTracker()
	svc := withKiroCacheScopeClient(t, true)
	group := kiroCacheGroup(1)
	acct := kiroCacheAccount(1, "r", "a")
	_ = svc.buildKiroCacheEmulationUsage(kiroCacheKeyCtx(9), acct, group, kiroCacheBodyWithUserID(t, "iso", "user_x_account__session_aaaa-1111"), "claude-sonnet-4-6", 2000)

	otherSession := svc.buildKiroCacheEmulationUsage(kiroCacheKeyCtx(9), acct, group, kiroCacheBodyWithUserID(t, "iso", "user_x_account__session_bbbb-2222"), "claude-sonnet-4-6", 2000)
	require.NotNil(t, otherSession)
	require.Zero(t, otherSession.CacheReadInputTokens, "不同 session 不应命中")

	otherKey := svc.buildKiroCacheEmulationUsage(kiroCacheKeyCtx(10), acct, group, kiroCacheBodyWithUserID(t, "iso", "user_x_account__session_aaaa-1111"), "claude-sonnet-4-6", 2000)
	require.NotNil(t, otherKey)
	require.Zero(t, otherKey.CacheReadInputTokens, "不同 API key 不应命中")
}

func TestKiroCacheScopeClientWithoutSessionUsesDerivedConversation(t *testing.T) {
	resetKiroCacheTracker()
	svc := withKiroCacheScopeClient(t, true)
	group := kiroCacheGroup(1)
	body := kiroCacheBodyWithUserID(t, "no session", "")
	_ = svc.buildKiroCacheEmulationUsage(kiroCacheKeyCtx(9), kiroCacheAccount(1, "r", "a"), group, body, "claude-sonnet-4-6", 2000)
	other := svc.buildKiroCacheEmulationUsage(kiroCacheKeyCtx(9), kiroCacheAccount(2, "r", "a"), group, body, "claude-sonnet-4-6", 2000)
	require.NotNil(t, other)
	require.Equal(t, 2000, other.CacheReadInputTokens)
}

func TestKiroCacheScopeClientWithoutAPIKeyFallsBackToAccount(t *testing.T) {
	resetKiroCacheTracker()
	svc := withKiroCacheScopeClient(t, true)
	group := kiroCacheGroup(1)
	body := kiroCacheBodyWithUserID(t, "no key", "user_x_account__session_aaaa-1111")
	_ = svc.buildKiroCacheEmulationUsage(context.Background(), kiroCacheAccount(1, "r", "a"), group, body, "claude-sonnet-4-6", 2000)
	other := svc.buildKiroCacheEmulationUsage(context.Background(), kiroCacheAccount(2, "r", "a"), group, body, "claude-sonnet-4-6", 2000)
	require.NotNil(t, other)
	require.Zero(t, other.CacheReadInputTokens, "没有 API key 时应按账号分桶")
}

func TestKiroCacheSessionID(t *testing.T) {
	cases := map[string]string{
		`{"metadata":{"user_id":"user_a_account__session_0b44-e1ff"}}`:            "0b44-e1ff",
		`{"metadata":{"user_id":"user_a_account__session_abc-XYZ"}}`:              "abc-XYZ",
		`{"metadata":{"user_id":"user_a_account__session_aaaa|tail"}}`:            "aaaa|tail",
		`{"metadata":{"user_id":"session_550e8400-e29b"}}`:                        "550e8400-e29b",
		`{"metadata":{"user_id":"{\"session_id\":\"js-1\"}"}}`:                    "js-1",
		`{"metadata":{"user_id":"{\"device_id\":\"d\",\"note\":\"session_z\"}"}}`: "",
		`{"prompt_cache_key":"codex-conv-1"}`:                                     "codex-conv-1",
		`{"metadata":{"user_id":"plain-user"}}`:                                   "",
		`{}`:                                                                      "",
	}
	for body, want := range cases {
		require.Equal(t, want, kiroCacheSessionID([]byte(body)), body)
	}
}

// 没有会话标识时，同一个 API key 下的不同对话即使系统提示相同也不能互相命中。
func TestKiroCacheScopeClientDerivedConversationIsolatesUsers(t *testing.T) {
	resetKiroCacheTracker()
	svc := withKiroCacheScopeClient(t, true)
	group := kiroCacheGroup(1)
	ctx := kiroCacheKeyCtx(9)
	_ = svc.buildKiroCacheEmulationUsage(ctx, kiroCacheAccount(1, "r", "a"), group, kiroCacheSharedSystemBody("customer A question"), "claude-sonnet-4-6", 6000)

	other := svc.buildKiroCacheEmulationUsage(ctx, kiroCacheAccount(1, "r", "a"), group, kiroCacheSharedSystemBody("customer B question"), "claude-sonnet-4-6", 6000)
	require.NotNil(t, other)
	require.Zero(t, other.CacheReadInputTokens, "不同对话不应命中彼此的系统提示")

	nextTurn := svc.buildKiroCacheEmulationUsage(ctx, kiroCacheAccount(2, "r", "a"), group, kiroCacheSharedSystemBody("customer A question", "ok", "follow up"), "claude-sonnet-4-6", 6000)
	require.NotNil(t, nextTurn)
	require.Positive(t, nextTurn.CacheReadInputTokens, "同一对话下一轮换账号仍应命中")
}

func TestKiroCacheDerivedConversation(t *testing.T) {
	asString := kiroCacheDerivedConversation([]byte(`{"system":"sys","messages":[{"role":"user","content":"hello"}]}`))
	require.NotEmpty(t, asString)
	asBlocks := kiroCacheDerivedConversation([]byte(`{"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}]}`))
	require.Equal(t, asString, asBlocks, "字符串与单个 text 块、cache_control 位置不应影响结果")
	laterTurn := kiroCacheDerivedConversation([]byte(`{"system":"sys","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"more"}]}`))
	require.Equal(t, asString, laterTurn, "后续轮次不应改变派生值")
	require.NotEqual(t, asString, kiroCacheDerivedConversation([]byte(`{"system":"sys","messages":[{"role":"user","content":"other"}]}`)))
	require.NotEqual(t, asString, kiroCacheDerivedConversation([]byte(`{"system":"other","messages":[{"role":"user","content":"hello"}]}`)))

	chat := kiroCacheDerivedConversation([]byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"}]}`))
	responses := kiroCacheDerivedConversation([]byte(`{"instructions":"sys","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`))
	responsesString := kiroCacheDerivedConversation([]byte(`{"instructions":"sys","input":"hello"}`))
	require.Equal(t, asString, chat)
	require.Equal(t, asString, responses)
	require.Equal(t, asString, responsesString)

	require.Empty(t, kiroCacheDerivedConversation([]byte(`{"system":"sys","messages":[]}`)))
}

// kiroCacheSharedSystemBody 构造系统提示带断点、无会话标识的请求；turns 依次为
// user、assistant、user… 的文本。
func kiroCacheSharedSystemBody(turns ...string) []byte {
	system := strings.Repeat("shared system prompt chunk ", 600)
	messages := make([]map[string]any, 0, len(turns))
	for i, text := range turns {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, map[string]any{"role": role, "content": strings.Repeat(text+" ", 50)})
	}
	out, _ := json.Marshal(map[string]any{
		"model":    "claude-sonnet-4-6",
		"system":   []map[string]any{{"type": "text", "text": system, "cache_control": map[string]any{"type": "ephemeral"}}},
		"messages": messages,
	})
	return out
}
