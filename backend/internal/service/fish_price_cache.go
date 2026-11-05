package service

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/fish"
)

// fishPriceTTL 是单价观测的有效期。价格表全局共享（与账号、套餐无关），
// 但 pricingVersion 会变，过期后等下一次 Price 调用重新学到。
const fishPriceTTL = 6 * time.Hour

// fishPriceKey 是 /tools/image-video/price 的计价维度（张数除外：计价按张
// 线性，缓存每张单价）。
type fishPriceKey struct {
	model         string
	variant       string
	imageSize     string
	quality       string
	refImageCount int
}

type fishPriceEntry struct {
	perItem    int
	observedAt time.Time
}

// fishPriceCache 是进程内的单价观测表。它只用于选号前过滤和「余额耗尽」
// 判定；提交时的 ExpectedCredits / PricingVersion 仍取紧邻的实时 Price。
type fishPriceCache struct {
	mu      sync.RWMutex
	entries map[fishPriceKey]fishPriceEntry
}

// fishPrices 是进程级单价表：生图服务写入，选号与 402 处理读取。
var fishPrices = &fishPriceCache{entries: make(map[fishPriceKey]fishPriceEntry)}

// fishPerItemCredits 返回一次计价的每张单价；上游没给 perItemCredits 时按
// 总价 / 张数向上取整。
func fishPerItemCredits(price *fish.PriceResult, count int) int {
	if price == nil {
		return 0
	}
	if price.PerItemCredits > 0 {
		return price.PerItemCredits
	}
	if count <= 0 || price.Credits <= 0 {
		return 0
	}
	return (price.Credits + count - 1) / count
}

func (c *fishPriceCache) record(key fishPriceKey, price *fish.PriceResult, count int, now time.Time) {
	perItem := fishPerItemCredits(price, count)
	if perItem <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = fishPriceEntry{perItem: perItem, observedAt: now}
}

func (c *fishPriceCache) perItem(key fishPriceKey, now time.Time) (int, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok || now.Sub(entry.observedAt) > fishPriceTTL {
		return 0, false
	}
	return entry.perItem, true
}

// minPerItem 返回有效期内观测到的最低单价——付不起它的账号付不起任何一单。
func (c *fishPriceCache) minPerItem(now time.Time) (int, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	lowest := 0
	for _, entry := range c.entries {
		if now.Sub(entry.observedAt) > fishPriceTTL {
			continue
		}
		if lowest == 0 || entry.perItem < lowest {
			lowest = entry.perItem
		}
	}
	return lowest, lowest > 0
}

// fishExhaustedBelow 返回「余额耗尽」的界线：低于最便宜一单的单价。还没
// 观测到任何价格时退化为余额 <= 0。
func fishExhaustedBelow(now time.Time) int {
	if lowest, ok := fishPrices.minPerItem(now); ok {
		return lowest
	}
	return 1
}

// fishCallPriceKey 由请求参数推出计价键（画质未按账号上限压低）。参数不合法
// 时返回 false——那种请求会在 GenerateCall 里按请求错误终止，不必过滤。
func fishCallPriceKey(call *FishImageCall) (fishPriceKey, bool) {
	if call == nil || call.Request == nil {
		return fishPriceKey{}, false
	}
	parsed := call.Request
	model, ok := domain.LookupFishModel(call.modelName())
	if !ok {
		return fishPriceKey{}, false
	}
	plan, err := domain.FishPlanSize(parsed.Size, call.fitLimits)
	if err != nil {
		return fishPriceKey{}, false
	}
	quality, err := fishQualityForRequest(parsed.Quality, model.ExtendedQuality)
	if err != nil {
		return fishPriceKey{}, false
	}
	key := fishPriceKey{model: model.Name, variant: fish.VariantTextToImage, imageSize: plan.Native, quality: quality}
	if wantsFishImageEdits(parsed) {
		key.variant = fish.VariantEdit
		key.refImageCount = len(parsed.Uploads) + len(parsed.InputImageURLs)
	}
	return key, true
}

type fishCreditRequirementContextKey struct{}

// fishCreditRequirement 是一次 fish 生图请求在选号时的余额门槛。
type fishCreditRequirement struct {
	key fishPriceKey
	// want 是这一单还要出的张数（换号续做时为剩余张数）。
	want   int
	policy FishCreditPolicy
	now    time.Time
}

// WithFishCreditRequirement 把本次请求的计价键挂到选号 context 上，让选号
// 排除已知付不起的账号、优先用零头。开关关闭或单价未知时原样返回 ctx，选号
// 行为与旧版一致。
func WithFishCreditRequirement(ctx context.Context, call *FishImageCall) context.Context {
	if call == nil || !call.creditPolicy.Enabled {
		return ctx
	}
	key, ok := fishCallPriceKey(call)
	if !ok {
		return ctx
	}
	now := time.Now()
	if _, known := fishPrices.perItem(key, now); !known {
		return ctx
	}
	requirement := fishCreditRequirement{key: key, want: max(call.remaining, 1), policy: call.creditPolicy, now: now}
	return context.WithValue(ctx, fishCreditRequirementContextKey{}, requirement)
}

// perItemFor 返回这个账号出一张图的单价：账号画质上限低于请求时按压低后的
// 档位查价；该档位没有观测时返回 false（不过滤）。
func (r fishCreditRequirement) perItemFor(account *Account) (int, bool) {
	key := r.key
	key.quality = fishCapQuality(key.quality, account.FishMaxQuality())
	return fishPrices.perItem(key, r.now)
}

// fishCreditTier 给账号的余额与本单的匹配度分档（越小越优先）。能一次付清
// 剩余张数的号排前面，按付完后还剩几张分档、剩得少的先用（消化零头）：
// 0 = 剩 0–2 张，1 = 剩 3–14 张，2 = 剩 15 张以上；3 = 余额未知或不适用；
// 4 = 只够出其中一部分。多张图的单交给付不清的号会出一批、换号、再出一批，
// 每次换号都要等上一批出完，所以排在余额未知的号之后（未知号付不起时提交
// 立刻 402，换号代价小）。单张的单不会落到第 4 档，分档与「零头优先」一致。
// context 里没有门槛时所有账号同档，排序不变。
func fishCreditTier(ctx context.Context, account *Account) int {
	const (
		unknownTier = 3
		partialTier = 4
	)
	requirement, ok := ctx.Value(fishCreditRequirementContextKey{}).(fishCreditRequirement)
	if !ok || !isFishTokenAccount(account) {
		return unknownTier
	}
	perItem, ok := requirement.perItemFor(account)
	if !ok || perItem <= 0 {
		return unknownTier
	}
	balance, ok := requirement.policy.trustedBalance(account, requirement.now)
	if !ok {
		return unknownTier
	}
	surplus := balance/perItem - max(requirement.want, 1)
	switch {
	case surplus < 0:
		return partialTier
	case surplus < 3:
		return 0
	case surplus < 15:
		return 1
	default:
		return 2
	}
}

// isAccountAffordableForFishJob 报告账号是否可能付得起本单的一张图。只有
// 确知付不起（可信余额 < 单价，或近期 402 记下的「余额 < X」且 X <= 单价）
// 才排除；中转号、余额未知、没有门槛的请求一律放行。
func isAccountAffordableForFishJob(ctx context.Context, account *Account) bool {
	requirement, ok := ctx.Value(fishCreditRequirementContextKey{}).(fishCreditRequirement)
	if !ok || !isFishTokenAccount(account) {
		return true
	}
	perItem, ok := requirement.perItemFor(account)
	if !ok || perItem <= 0 {
		return true
	}
	return !requirement.policy.cannotAfford(account, perItem, requirement.now)
}
