package service

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAccountKiroDefaultMappingRestrictsUnsupportedModels(t *testing.T) {
	account := &Account{Platform: PlatformKiro, Type: AccountTypeOAuth}

	require.False(t, account.IsModelSupported("gpt-4o"))
	require.False(t, account.IsModelSupported("kiro-gpt-4o"))
	require.False(t, account.IsModelSupported("auto"))
	require.Equal(t, "claude-sonnet-4.6", account.GetMappedModel("claude-sonnet-4-6"))
}

func TestKiroDirectClaudeAliasFoldsDotPlacement(t *testing.T) {
	account := &Account{
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-opus-4-8":          "claude-opus-4.8",
				"claude-opus-4-8-thinking": "claude-opus-4.8",
				"codex-auto-review":        "gpt-5.6-luna",
			},
		},
	}

	for _, requested := range []string{
		"claude-opus-4-8",
		"claude-opus-4.8",
		"claude-opus.4-8",
		"claude-opus-4-8-thinking",
		"claude-opus-4.8-thinking",
	} {
		require.True(t, account.IsModelSupported(requested), requested)
		require.Equal(t, "claude-opus-4.8", account.GetMappedModel(requested), requested)
	}
	require.Equal(t, "gpt-5.6-luna", account.GetMappedModel("codex-auto-review"))
	require.False(t, account.IsModelSupported("gpt-5.6.sol"))
	require.False(t, account.IsModelSupported("claude-opus-4-5-20251101"))
	require.False(t, account.IsModelSupported("claude-opus-4-5-20990101"))

	defaults := &Account{Platform: PlatformKiro, Type: AccountTypeOAuth}
	require.True(t, defaults.IsModelSupported("claude-opus-4-5-20251101"))
	require.Equal(t, "claude-opus-4.5", defaults.GetMappedModel("claude-opus-4-5-20251101"))
	require.False(t, defaults.IsModelSupported("claude-opus-4-5-20990101"))
}

func TestKiroDirectClaudeAliasPrefersExactCustomKey(t *testing.T) {
	account := &Account{
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-opus-4-8": "claude-sonnet-4.6",
				"my-opus":         "claude-opus-4.8",
			},
		},
	}

	require.Equal(t, "claude-sonnet-4.6", account.GetMappedModel("claude-opus-4.8"))
	require.Equal(t, "claude-sonnet-4.6", account.GetMappedModel("claude-opus.4-8"))
	require.Equal(t, "claude-opus-4.8", account.GetMappedModel("my-opus"))
}

func TestKiroRelayDoesNotFoldClaudeAliases(t *testing.T) {
	account := &Account{
		Platform: PlatformKiro,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://relay.example",
			"model_mapping": map[string]any{
				"claude-opus-4-8": "claude-opus-4-8",
			},
		},
	}

	require.True(t, account.IsModelSupported("claude-opus-4-8"))
	require.Equal(t, "claude-opus-4-8", account.GetMappedModel("claude-opus-4-8"))
	require.False(t, account.IsModelSupported("claude-opus-4.8"))
	require.False(t, account.IsModelSupported("claude-opus.4-8"))
}

func TestKiroPublicCatalogKeepsUpstreamModelID(t *testing.T) {
	names, metadata := kiroPublicCatalog([]string{"claude-opus-4.8.1", "gpt-5.6-sol"})
	require.Equal(t, []string{
		"claude-opus-4-8-1",
		"claude-opus-4-8-1-thinking",
		"gpt-5.6-sol",
	}, names)
	require.Equal(t, "claude-opus-4.8.1", metadata["claude-opus-4-8-1"].ID)
	require.Equal(t, "claude-opus-4.8.1", metadata["claude-opus-4-8-1-thinking"].ID)
	require.Equal(t, "gpt-5.6-sol", metadata["gpt-5.6-sol"].ID)
}

func TestKiroPublicCatalogAddsHaikuDatedAlias(t *testing.T) {
	names, metadata := kiroPublicCatalog([]string{"claude-haiku-4.5"})
	require.Equal(t, []string{
		"claude-haiku-4-5",
		"claude-haiku-4-5-thinking",
		"claude-haiku-4-5-20251001",
		"claude-haiku-4-5-20251001-thinking",
	}, names)
	require.Equal(t, "claude-haiku-4.5", metadata["claude-haiku-4-5-20251001"].ID)
	require.Equal(t, "claude-haiku-4.5", metadata["claude-haiku-4-5-20251001-thinking"].ID)
}

func TestKiroIdentityMappingStillResolvesDottedUpstreamID(t *testing.T) {
	account := &Account{
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-opus-4-8": "claude-opus-4-8",
			},
		},
	}

	require.Equal(t, "claude-opus-4.8", resolveKiroUpstreamModel(account.GetMappedModel("claude-opus-4-8")))
}

func TestKiroGetMappedModelKeepsStoredDottedUpstreamID(t *testing.T) {
	account := &Account{
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-opus-4-5":          "claude-opus-4.5",
				"claude-opus-4-5-thinking": "claude-sonnet-4.6",
			},
		},
	}

	require.Equal(t, "claude-opus-4.5", account.GetMappedModel("claude-opus-4-5"))
	require.Equal(t, "claude-sonnet-4.6", account.GetMappedModel("claude-opus-4-5-thinking"))
}

func TestGatewayServiceCalculateTokenCost_KiroAutoUsesConservativeFallback(t *testing.T) {
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1.1

	svc := NewGatewayService(
		nil,                         // accountRepo
		nil,                         // groupRepo
		nil,                         // usageLogRepo
		nil,                         // usageBillingRepo
		nil,                         // userRepo
		nil,                         // userSubRepo
		nil,                         // userGroupRateRepo
		nil,                         // cache
		cfg,                         // cfg
		nil,                         // schedulerSnapshot
		nil,                         // concurrencyService
		NewBillingService(cfg, nil), // billingService
		nil,                         // rateLimitService
		nil,                         // billingCacheService
		nil,                         // identityService
		nil,                         // httpUpstream
		nil,                         // deferredService
		nil,                         // claudeTokenProvider
		nil, nil,
		// kiroTokenProvider
		nil, nil, nil, // kiroCooldownStore
		nil, // sessionLimitCache
		nil, // rpmCache
		nil, // digestStore
		nil, // settingService
		nil, // tlsFPProfileService
		nil, // channelService
		nil, // resolver
		nil, // compositeResolver
		nil, // balanceNotifyService
		nil, // userPlatformQuotaRepo
	)

	result := &ForwardResult{
		Model:         "auto",
		UpstreamModel: "auto",
		Usage: ClaudeUsage{
			InputTokens:  20,
			OutputTokens: 10,
		},
	}

	expected, err := svc.billingService.CalculateCost(kiroConservativeFallbackBillingModel, UsageTokens{
		InputTokens:  20,
		OutputTokens: 10,
	}, 1.1)
	require.NoError(t, err)

	cost := svc.calculateTokenCost(context.Background(), result, &APIKey{}, "auto", 1.1, time.Time{}, &recordUsageOpts{IsKiroAccount: true})
	require.NotNil(t, cost)
	require.InDelta(t, expected.ActualCost, cost.ActualCost, 1e-12)
	require.InDelta(t, expected.TotalCost, cost.TotalCost, 1e-12)
}

func TestGatewayServiceCalculateTokenCost_KiroQwenUsesSonnetCreditRatio(t *testing.T) {
	svc := newKiroBillingGatewayForTest(t)
	svc.billingService = NewBillingService(&config.Config{}, loadModelPricingCatalog(t))
	tokens := UsageTokens{
		InputTokens:           250000,
		OutputTokens:          1000,
		CacheCreationTokens:   30,
		CacheCreation5mTokens: 20,
		CacheCreation1hTokens: 10,
		CacheReadTokens:       50,
	}
	ratio := 0.05 / 1.3
	result := &ForwardResult{
		Model:         "qwen3-coder-next",
		UpstreamModel: "qwen3-coder-next",
		Usage: ClaudeUsage{
			InputTokens:              tokens.InputTokens,
			OutputTokens:             tokens.OutputTokens,
			CacheCreationInputTokens: tokens.CacheCreationTokens,
			CacheCreation5mTokens:    tokens.CacheCreation5mTokens,
			CacheCreation1hTokens:    tokens.CacheCreation1hTokens,
			CacheReadInputTokens:     tokens.CacheReadTokens,
		},
	}

	cost := svc.calculateTokenCost(context.Background(), result, &APIKey{}, "qwen3-coder-next", 1.1, time.Time{}, &recordUsageOpts{IsKiroAccount: true})
	require.NotNil(t, cost)
	require.InDelta(t, float64(tokens.InputTokens)*3e-6*ratio, cost.InputCost, 1e-12)
	require.InDelta(t, float64(tokens.OutputTokens)*15e-6*ratio, cost.OutputCost, 1e-12)
	require.InDelta(t, float64(20)*3.75e-6*ratio+float64(10)*6e-6*ratio, cost.CacheCreationCost, 1e-12)
	require.InDelta(t, float64(tokens.CacheReadTokens)*0.3e-6*ratio, cost.CacheReadCost, 1e-12)
	require.InDelta(t, cost.TotalCost*1.1, cost.ActualCost, 1e-12)
	require.False(t, cost.LongContextBillingApplied)
}

func TestGatewayServiceCalculateTokenCost_KiroQwenGroupPricingWins(t *testing.T) {
	svc := newKiroBillingGatewayForTest(t)
	inputPrice := 9e-6
	outputPrice := 1e-6
	group := &Group{
		ID:       7,
		Platform: PlatformKiro,
		ModelPricing: []ChannelModelPricing{{
			Models:      []string{"qwen3-coder-next"},
			BillingMode: BillingModeToken,
			InputPrice:  &inputPrice,
			OutputPrice: &outputPrice,
		}},
	}
	svc.resolver = NewModelPricingResolver(nil, svc.billingService)
	result := &ForwardResult{
		Model: "qwen3-coder-next",
		Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 4},
	}

	cost := svc.calculateTokenCost(context.Background(), result, &APIKey{Group: group}, "qwen3-coder-next", 1.1, time.Time{}, &recordUsageOpts{IsKiroAccount: true})
	require.NotNil(t, cost)
	require.InDelta(t, 10*inputPrice, cost.InputCost, 1e-12)
	require.InDelta(t, 4*outputPrice, cost.OutputCost, 1e-12)
}

func TestGatewayServiceCalculateTokenCost_KiroQwenChannelPricingWins(t *testing.T) {
	svc := newKiroBillingGatewayForTest(t)
	groupID := int64(9)
	inputPrice := 4e-6
	outputPrice := 8e-6
	cache := newEmptyChannelCache()
	cache.pricingByGroupModel[channelModelKey{groupID: groupID, platform: PlatformKiro, model: "qwen3-coder-next"}] = &ChannelModelPricing{
		BillingMode: BillingModeToken,
		InputPrice:  &inputPrice,
		OutputPrice: &outputPrice,
	}
	cache.channelByGroupID[groupID] = &Channel{ID: 1, Status: StatusActive}
	cache.groupPlatform[groupID] = PlatformKiro
	cache.loadedAt = time.Now()
	channelService := &ChannelService{}
	channelService.cache.Store(cache)
	svc.resolver = NewModelPricingResolver(channelService, svc.billingService)
	group := &Group{ID: groupID, Platform: PlatformKiro}
	result := &ForwardResult{
		Model: "qwen3-coder-next",
		Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 4},
	}

	cost := svc.calculateTokenCost(context.Background(), result, &APIKey{Group: group}, "qwen3-coder-next", 1.1, time.Time{}, &recordUsageOpts{IsKiroAccount: true})
	require.NotNil(t, cost)
	require.InDelta(t, 10*inputPrice, cost.InputCost, 1e-12)
	require.InDelta(t, 4*outputPrice, cost.OutputCost, 1e-12)
}

func TestGatewayServiceCalculateTokenCost_NonKiroGLM5KeepsOfficialFallback(t *testing.T) {
	svc := newKiroBillingGatewayForTest(t)
	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 100}
	expected, err := svc.billingService.CalculateCost("glm-5", tokens, 1.1)
	require.NoError(t, err)

	result := &ForwardResult{
		Model: "glm-5",
		Usage: ClaudeUsage{InputTokens: tokens.InputTokens, OutputTokens: tokens.OutputTokens},
	}
	cost := svc.calculateTokenCost(context.Background(), result, &APIKey{}, "glm-5", 1.1, time.Time{})
	require.NotNil(t, cost)
	require.InDelta(t, 1000*1e-6, cost.InputCost, 1e-12)
	require.InDelta(t, 100*3.2e-6, cost.OutputCost, 1e-12)
	require.InDelta(t, expected.ActualCost, cost.ActualCost, 1e-12)
}

func TestGatewayServiceCalculateTokenCost_NonKiroQwenDoesNotUseCreditRatio(t *testing.T) {
	svc := newKiroBillingGatewayForTest(t)
	result := &ForwardResult{
		Model: "qwen3-coder-next",
		Usage: ClaudeUsage{InputTokens: 250000, OutputTokens: 1000},
	}

	cost := svc.calculateTokenCost(context.Background(), result, &APIKey{}, "qwen3-coder-next", 1.1, time.Time{})
	require.NotNil(t, cost)
	require.Zero(t, cost.ActualCost)
}

func loadModelPricingCatalog(t *testing.T) *PricingService {
	t.Helper()
	data, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	require.NoError(t, err)
	catalog := &PricingService{}
	catalog.pricingData, err = catalog.parsePricingData(data)
	require.NoError(t, err)
	return catalog
}

func newKiroBillingGatewayForTest(t *testing.T) *GatewayService {
	t.Helper()
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1.1
	return NewGatewayService(
		nil, nil, nil, nil, nil, nil, nil, nil,
		cfg,
		nil, nil,
		NewBillingService(cfg, nil),
		nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
}
