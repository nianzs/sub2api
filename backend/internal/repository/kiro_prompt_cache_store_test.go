//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func newKiroPromptCacheTestStore(t *testing.T) (*KiroPromptCacheStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewKiroPromptCacheRemoteStore(rdb), mr
}

func kiroPromptCacheFP(seed byte) [32]byte {
	var out [32]byte
	for i := range out {
		out[i] = seed + byte(i)
	}
	return out
}

func TestKiroPromptCacheStoreNilClientUnavailable(t *testing.T) {
	s := NewKiroPromptCacheRemoteStore(nil)
	_, err := s.Lookup(context.Background(), 1, [][32]byte{kiroPromptCacheFP(1)})
	require.ErrorIs(t, err, ErrKiroPromptCacheStoreUnavailable)
	require.ErrorIs(t, s.Store(context.Background(), 1, kiroPromptCacheFP(1), service.KiroPromptCacheEntry{PromptTokens: 1, StartedAt: time.Now()}), ErrKiroPromptCacheStoreUnavailable)
	require.Nil(t, ProvideKiroPromptCacheRemoteStore(nil))
}

func TestKiroPromptCacheStoreRoundTripAndPartialLookup(t *testing.T) {
	s, mr := newKiroPromptCacheTestStore(t)
	ctx := context.Background()
	started := time.UnixMilli(time.Now().UnixMilli())
	require.NoError(t, s.Store(ctx, 7, kiroPromptCacheFP(1), service.KiroPromptCacheEntry{PromptTokens: 97000, StartedAt: started}))

	got, err := s.Lookup(ctx, 7, [][32]byte{kiroPromptCacheFP(0), kiroPromptCacheFP(1)})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, 97000, got[kiroPromptCacheFP(1)].PromptTokens)
	require.True(t, got[kiroPromptCacheFP(1)].StartedAt.Equal(started))

	// 不同账号互不可见。
	other, err := s.Lookup(ctx, 8, [][32]byte{kiroPromptCacheFP(1)})
	require.NoError(t, err)
	require.Empty(t, other)

	// key 带过期时间，只用于清理。
	ttl := mr.TTL(kiroPromptCacheKey(7, kiroPromptCacheFP(1)))
	require.Equal(t, kiroPromptCacheKeyTTL, ttl)
}

func TestKiroPromptCacheStoreLookupErrorWhenRedisDown(t *testing.T) {
	s, mr := newKiroPromptCacheTestStore(t)
	mr.Close()
	_, err := s.Lookup(context.Background(), 7, [][32]byte{kiroPromptCacheFP(1)})
	require.Error(t, err)
}
