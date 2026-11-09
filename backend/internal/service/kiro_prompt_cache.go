package service

import (
	"context"
	"sync"
	"time"

	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// Kiro 提示词缓存推断：为用 credits 反推 output 提供命中量 C。
//
// 实测 Kiro 的提示词缓存只在新提示词完整延续之前某个请求的完整提示词时命中
// （同账号、同模型、只在末尾追加），有效期 5 分钟，从那个请求发出时算起。
// 所以每个请求完成后记录"完整提示词指纹 → {提示词大小 P, 发出时间}"；下一个
// 请求发出时，在自己的前缀指纹里找最深的、5 分钟内发出的记录，命中量就是
// 那个请求的 P。指纹由 translator 按 Kiro 实际收到的内容算（KiroRequestContext.
// PromptFingerprints）。和计费用的缓存模拟（kiro_cache_emulation.go）互不相干。

// kiroPromptCacheTTL 是 Kiro 提示词缓存的有效期（生产数据实测）。
const kiroPromptCacheTTL = 5 * time.Minute

// kiroPromptCacheRemoteWriteTimeout 是后台写入远端存储的超时。
const kiroPromptCacheRemoteWriteTimeout = time.Second

// KiroPromptCacheEntry 是一个请求的完整提示词记录。
type KiroPromptCacheEntry struct {
	PromptTokens int       // 提示词大小，Kiro token 单位
	StartedAt    time.Time // 请求发往 Kiro 的时刻
}

// KiroPromptCacheRemoteStore 是跨实例共享的提示词记录存储（Redis）。
// 出错时调用方按本地内存继续；远端出错且本地也没有记录时视为未知，不做推断。
type KiroPromptCacheRemoteStore interface {
	Lookup(ctx context.Context, accountID int64, fingerprints [][32]byte) (map[[32]byte]KiroPromptCacheEntry, error)
	Store(ctx context.Context, accountID int64, fp [32]byte, entry KiroPromptCacheEntry) error
}

// kiroPromptCacheAsyncDispatch 把远端写入派发到后台，测试可替换为同步执行。
var kiroPromptCacheAsyncDispatch = func(fn func()) { go fn() }

type kiroPromptCacheKey struct {
	accountID int64
	fp        [32]byte
}

// kiroPromptCacheMemory 是进程内兜底，Redis 不可用时仍能覆盖落在同一实例的连续请求。
type kiroPromptCacheMemory struct {
	mu        sync.Mutex
	entries   map[kiroPromptCacheKey]KiroPromptCacheEntry
	lastPrune time.Time
}

// 内存兜底的清理参数（变量便于测试覆盖）：条目数达到 pruneAt 后最多每 pruneInterval
// 扫一次过期条目，避免每次写入都持锁全表扫描；仍超过 hardCap 时整表清空——它只是
// Redis 的兜底，清空的代价只是少几次命中推断。
var (
	kiroPromptCacheMemoryPruneAt       = 8192
	kiroPromptCacheMemoryHardCap       = 65536
	kiroPromptCacheMemoryPruneInterval = time.Minute
)

var globalKiroPromptCacheMemory = &kiroPromptCacheMemory{entries: map[kiroPromptCacheKey]KiroPromptCacheEntry{}}

func (m *kiroPromptCacheMemory) lookup(accountID int64, fps [][32]byte) map[[32]byte]KiroPromptCacheEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[[32]byte]KiroPromptCacheEntry{}
	for _, fp := range fps {
		if e, ok := m.entries[kiroPromptCacheKey{accountID, fp}]; ok {
			out[fp] = e
		}
	}
	return out
}

func (m *kiroPromptCacheMemory) store(accountID int64, fp [32]byte, entry KiroPromptCacheEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if len(m.entries) >= kiroPromptCacheMemoryPruneAt && now.Sub(m.lastPrune) >= kiroPromptCacheMemoryPruneInterval {
		m.lastPrune = now
		cutoff := now.Add(-2 * kiroPromptCacheTTL)
		for k, e := range m.entries {
			if e.StartedAt.Before(cutoff) {
				delete(m.entries, k)
			}
		}
	}
	if len(m.entries) >= kiroPromptCacheMemoryHardCap {
		m.entries = make(map[kiroPromptCacheKey]KiroPromptCacheEntry)
	}
	m.entries[kiroPromptCacheKey{accountID, fp}] = entry
}

// selectKiroPromptCacheMatch 从最深的前缀往回找，返回第一个在 sentAt 之前
// 5 分钟内发出的记录的提示词大小；没有则返回 0。
func selectKiroPromptCacheMatch(fps [][32]byte, entries map[[32]byte]KiroPromptCacheEntry, sentAt time.Time) int {
	for i := len(fps) - 1; i >= 0; i-- {
		e, ok := entries[fps[i]]
		if !ok {
			continue
		}
		age := sentAt.Sub(e.StartedAt)
		if age > 0 && age < kiroPromptCacheTTL {
			return e.PromptTokens
		}
	}
	return 0
}

// resolveKiroPromptCache 在请求发出后、读取响应前推断命中量，写入 requestCtx.PromptCache。
// 没有指纹（模型无单价或已关闭）时保持未解析，translator 沿用旧算法。
func (s *GatewayService) resolveKiroPromptCache(ctx context.Context, account *Account, requestCtx *kiropkg.KiroRequestContext) {
	if s == nil || account == nil || requestCtx == nil || len(requestCtx.PromptFingerprints) == 0 {
		return
	}
	fps := requestCtx.PromptFingerprints
	sentAt := requestCtx.UpstreamSentAt
	if sentAt.IsZero() {
		sentAt = time.Now()
	}
	entries := globalKiroPromptCacheMemory.lookup(account.ID, fps)
	if s.kiroPromptCacheRemote != nil {
		remote, err := s.kiroPromptCacheRemote.Lookup(ctx, account.ID, fps)
		if err != nil && len(entries) == 0 {
			// 远端不可用且本地没有记录：可能是另一个实例处理过上一轮，不能当成未命中。
			return
		}
		for fp, e := range remote {
			if cur, ok := entries[fp]; !ok || e.StartedAt.After(cur.StartedAt) {
				entries[fp] = e
			}
		}
	}
	requestCtx.PromptCache = kiropkg.KiroPromptCacheHint{
		Resolved:     true,
		CachedTokens: selectKiroPromptCacheMatch(fps, entries, sentAt),
	}
}

// recordKiroPromptCache 在请求完成后记录本次完整提示词的大小，供下一轮推断命中量。
// credits 反推成功时 P 是精确值；没用上 credits 时用"总量 - 本地可见输出"近似，
// 保证会话里一次反推失败不会让后续请求一直缺少命中记录（近似误差对下一轮 output
// 的影响约为误差的 4%）。上下文打满时总量不可信，不记录。
func (s *GatewayService) recordKiroPromptCache(account *Account, requestCtx kiropkg.KiroRequestContext, est kiropkg.KiroOutputEstimate) {
	if s == nil || account == nil || len(requestCtx.PromptFingerprints) == 0 || est.ContextTokens <= 0 {
		return
	}
	switch est.Reason {
	case "context_full", "no_rates":
		return
	}
	prompt := est.PromptTokens
	if est.Method != kiropkg.KiroOutputMethodCredits {
		prompt = est.ContextTokens - est.VisibleOutputTokens
	}
	if prompt <= 0 {
		return
	}
	startedAt := requestCtx.UpstreamSentAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	fp := requestCtx.PromptFingerprints[len(requestCtx.PromptFingerprints)-1]
	entry := KiroPromptCacheEntry{PromptTokens: prompt, StartedAt: startedAt}
	globalKiroPromptCacheMemory.store(account.ID, fp, entry)
	if remote := s.kiroPromptCacheRemote; remote != nil {
		accountID := account.ID
		kiroPromptCacheAsyncDispatch(func() {
			ctx, cancel := context.WithTimeout(context.Background(), kiroPromptCacheRemoteWriteTimeout)
			defer cancel()
			_ = remote.Store(ctx, accountID, fp, entry)
		})
	}
}

// logKiroOutputEstimate 记录 output 的来源。
//   - credits 反推被合理性检查否决时打 warn：比例升高通常意味着 Kiro 调价，需要重测
//     kiroCreditRateTable 里的单价。
//   - 有推断的命中量却没用上（改按整段命中或全部未命中）时打 info：多是上一轮没用上
//     credits、记录的提示词偏大，属正常自愈；但如果是 Kiro 实际没命中缓存，按整段命中
//     会让这一轮 output 多算约 4.3% × 缓存量。这条日志的占比用来估计后者有多常见。
func logKiroOutputEstimate(ctx context.Context, account *Account, model string, requestCtx kiropkg.KiroRequestContext, est kiropkg.KiroOutputEstimate) {
	if len(requestCtx.PromptFingerprints) == 0 || account == nil {
		return
	}
	fields := []zap.Field{
		zap.Int64("account_id", account.ID),
		zap.String("model", model),
		zap.String("method", est.Method),
		zap.String("reason", est.Reason),
		zap.Float64("credits", est.Credits),
		zap.Int("context_tokens", est.ContextTokens),
		zap.Int("prompt_tokens", est.PromptTokens),
		zap.Int("hinted_cached_tokens", requestCtx.PromptCache.CachedTokens),
		zap.Int("cached_tokens", est.CachedPromptTokens),
		zap.String("cache_assumption", est.CacheAssumption),
		zap.Int("visible_output_tokens", est.VisibleOutputTokens),
		zap.Int("output_tokens", est.OutputTokens),
	}
	if est.Reason == "out_of_bounds" {
		logger.FromContext(ctx).Warn("kiro.output_from_credits_rejected", fields...)
		return
	}
	if est.Method == kiropkg.KiroOutputMethodCredits && requestCtx.PromptCache.CachedTokens > 0 &&
		est.CacheAssumption != kiropkg.KiroCacheAssumptionHint {
		logger.FromContext(ctx).Info("kiro.output_cache_hint_overridden", fields...)
		return
	}
	logger.FromContext(ctx).Debug("kiro.output_estimate", fields...)
}
