package repository

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Kiro 提示词缓存推断的 Redis 存储：记录每个账号最近请求的完整提示词指纹
// → {提示词大小, 发出时间}，多实例共享，供下一轮推断 Kiro 缓存命中量。
// 有效期判定（5 分钟，从发出时间算）由 service 按值里的时间戳做，key 的 EX
// 只负责清理。

const (
	kiroPromptCacheKeyPrefix   = "kiro:promptcache:v1:"
	kiroPromptCacheOpTimeout   = 250 * time.Millisecond
	kiroPromptCacheKeyTTL      = 10 * time.Minute
	kiroPromptCacheLogThrottle = 10 * time.Second
)

// ErrKiroPromptCacheStoreUnavailable 表示未配置 Redis client。
var ErrKiroPromptCacheStoreUnavailable = errors.New("kiro prompt cache store unavailable")

type kiroPromptCacheValue struct {
	PromptTokens int   `json:"p"`
	StartedAtMs  int64 `json:"s"`
}

// KiroPromptCacheStore 是 service.KiroPromptCacheRemoteStore 的 Redis 实现。
type KiroPromptCacheStore struct {
	rdb     *redis.Client
	lastLog atomic.Int64
}

// NewKiroPromptCacheRemoteStore 返回 Redis 存储；rdb 为 nil 时方法返回
// ErrKiroPromptCacheStoreUnavailable。
func NewKiroPromptCacheRemoteStore(rdb *redis.Client) *KiroPromptCacheStore {
	return &KiroPromptCacheStore{rdb: rdb}
}

func kiroPromptCacheKey(accountID int64, fp [32]byte) string {
	return kiroPromptCacheKeyPrefix + strconv.FormatInt(accountID, 10) + ":" + hex.EncodeToString(fp[:])
}

// Lookup 一次 MGET 查出这些指纹中已记录的条目；缺失的指纹不出现在结果里。
func (s *KiroPromptCacheStore) Lookup(ctx context.Context, accountID int64, fingerprints [][32]byte) (map[[32]byte]service.KiroPromptCacheEntry, error) {
	if s == nil || s.rdb == nil {
		return nil, ErrKiroPromptCacheStoreUnavailable
	}
	if len(fingerprints) == 0 {
		return map[[32]byte]service.KiroPromptCacheEntry{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, kiroPromptCacheOpTimeout)
	defer cancel()

	keys := make([]string, len(fingerprints))
	for i, fp := range fingerprints {
		keys[i] = kiroPromptCacheKey(accountID, fp)
	}
	values, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		s.throttledWarn("kiro prompt cache redis lookup failed", err)
		return nil, err
	}
	out := make(map[[32]byte]service.KiroPromptCacheEntry, len(values))
	for i, raw := range values {
		str, ok := raw.(string)
		if !ok {
			continue
		}
		var v kiroPromptCacheValue
		if json.Unmarshal([]byte(str), &v) != nil || v.PromptTokens <= 0 || v.StartedAtMs <= 0 {
			continue
		}
		out[fingerprints[i]] = service.KiroPromptCacheEntry{
			PromptTokens: v.PromptTokens,
			StartedAt:    time.UnixMilli(v.StartedAtMs),
		}
	}
	return out, nil
}

// Store 写入（覆盖）一个完整提示词指纹的条目。
func (s *KiroPromptCacheStore) Store(ctx context.Context, accountID int64, fp [32]byte, entry service.KiroPromptCacheEntry) error {
	if s == nil || s.rdb == nil {
		return ErrKiroPromptCacheStoreUnavailable
	}
	value, err := json.Marshal(kiroPromptCacheValue{PromptTokens: entry.PromptTokens, StartedAtMs: entry.StartedAt.UnixMilli()})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, kiroPromptCacheOpTimeout)
	defer cancel()
	if err := s.rdb.Set(ctx, kiroPromptCacheKey(accountID, fp), value, kiroPromptCacheKeyTTL).Err(); err != nil {
		s.throttledWarn("kiro prompt cache redis store failed", err)
		return err
	}
	return nil
}

func (s *KiroPromptCacheStore) throttledWarn(msg string, err error) {
	now := time.Now().UnixNano()
	last := s.lastLog.Load()
	if now-last < int64(kiroPromptCacheLogThrottle) {
		return
	}
	if s.lastLog.CompareAndSwap(last, now) {
		slog.Warn(msg, "error", err)
	}
}
