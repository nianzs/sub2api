//go:build unit

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	kiro "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/stretchr/testify/require"
)

func TestKiroCachedZeroInputDoesNotFallback(t *testing.T) {
	for _, u := range []kiro.Usage{{CacheReadInputTokens: 1000}, {CacheCreationInputTokens: 1000}} {
		got := kiroUsageToClaude(u, 1000)
		require.Zero(t, got.InputTokens)
		require.Equal(t, 1000, got.CacheReadInputTokens+got.CacheCreationInputTokens)
	}
	require.Equal(t, 1000, kiroUsageToClaude(kiro.Usage{}, 1000).InputTokens)
}
func TestRetainedCachePlanConservesRatios(t *testing.T) {
	for index, tc := range []struct {
		name, mode     string
		creation, read float64
	}{
		{"uniform", KiroCacheEmulationModeUniform, .5, .5},
		{"independent", KiroCacheEmulationModeIndependent, .25, .75},
		{"read-only", KiroCacheEmulationModeIndependent, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{ID: int64(991011 + index), Platform: PlatformKiro, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "synthetic-retained-audit"}}
			group := &Group{Platform: PlatformKiro, KiroCacheEmulationEnabled: true, KiroCacheEmulationRatio: tc.creation, KiroCacheCreationEmulationRatio: tc.creation, KiroCacheReadEmulationRatio: tc.read, KiroCacheEmulationMode: tc.mode}
			svc := &GatewayService{}
			body := []byte(`{"model":"claude-opus-5-5-thinking","messages":[{"role":"user","content":"` + strings.Repeat("synthetic retained prefix ", 2000) + `"}]}`)
			plan := svc.prepareKiroCacheEmulationUsage(context.Background(), account, group, body, "claude-opus-5-5-thinking", 10000)
			require.NotNil(t, plan)
			rebuilt := svc.rebuildRetainedKiroCachePlan(context.Background(), account, plan, body, "claude-opus-5-5-thinking", 4000)
			require.NotNil(t, rebuilt)
			usage := rebuilt.result()
			if tc.creation == 0 {
				require.Nil(t, usage)
			} else {
				require.NotNil(t, usage)
				require.Equal(t, int(4000*tc.creation), usage.CacheCreationInputTokens)
				require.Equal(t, 4000, usage.InputTokens+usage.CacheReadInputTokens+usage.CacheCreationInputTokens)
			}
			rebuilt.commit()
			repeat := svc.rebuildRetainedKiroCachePlan(context.Background(), account, rebuilt, body, "claude-opus-5-5-thinking", 4000)
			require.NotNil(t, repeat)
			require.NotNil(t, repeat.result())
			require.Equal(t, int(4000*tc.read), repeat.result().CacheReadInputTokens)
			require.Zero(t, repeat.result().CacheCreationInputTokens)
			require.Equal(t, 4000, repeat.result().InputTokens+repeat.result().CacheReadInputTokens)
		})
	}
}

func TestRetainedInputMeteringConservationAfterTruncation(t *testing.T) {
	messages := []any{}
	for i := 0; i < 160; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, map[string]any{"role": role, "content": strings.Repeat("synthetic retained context ", 900)})
	}
	messages = append(messages, map[string]any{"role": "user", "content": "next"})
	raw, err := json.Marshal(map[string]any{"model": "claude-opus-5-5-thinking", "messages": messages})
	require.NoError(t, err)
	built, err := kiro.BuildKiroPayloadWithContext(raw, "claude-opus-5.5", "", "AI_EDITOR", nil)
	require.NoError(t, err)
	require.NotEmpty(t, built.Context.RetainedInputBody)
	ctx := context.Background()
	before := estimateKiroInputTokens(ctx, raw)
	after := estimateKiroInputTokens(ctx, built.Context.RetainedInputBody)
	require.Greater(t, before, after*3)
	account := &Account{ID: 991020, Platform: PlatformKiro, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "synthetic-final-payload-audit"}}
	group := &Group{Platform: PlatformKiro, KiroCacheEmulationEnabled: true, KiroCacheEmulationRatio: 0.5, KiroCacheCreationEmulationRatio: 0.5, KiroCacheReadEmulationRatio: 0.5, KiroCacheEmulationMode: KiroCacheEmulationModeUniform}
	svc := &GatewayService{}
	plan := svc.prepareKiroCacheEmulationUsage(ctx, account, group, built.Context.RetainedInputBody, "claude-opus-5-5-thinking", after)
	require.NotNil(t, plan)
	u := plan.result()
	require.NotNil(t, u)
	require.Equal(t, after, u.InputTokens+u.CacheReadInputTokens+u.CacheCreationInputTokens)
	require.InDelta(t, float64(after)/2, float64(u.InputTokens), 1)
	t.Logf("raw_estimate=%d retained_estimate=%d ordinary=%d cache=%d", before, after, u.InputTokens, u.CacheReadInputTokens+u.CacheCreationInputTokens)
}
