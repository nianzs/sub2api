//go:build unit

package service

import (
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
