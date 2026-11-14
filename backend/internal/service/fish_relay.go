package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// fish 中转账号的凭据键。
const (
	fishCredentialRelayBaseURL = "base_url"
	fishCredentialRelayAPIKey  = "api_key"
)

// fishMaxImagesPerGenerationRelay 与原生一致（模型 maxCount=8）；超过时
// 原生无法承接，走中转（OpenAI Images 上限 10）。
const fishRelayImageQuantityCap = 8

// IsFishRelayAccount 报告账号是否为 fish 中转号：platform=fish、apikey 型、
// 带 base_url。这类账号不打 fish 原生 API，而是把 /v1/images/generations|edits
// 原样转发到 {base_url}（官方 OpenAI 或三方兼容端点），承接 fish 原生
// 无法处理的请求（白名单外尺寸、mask、n>8、webp 等）。
// 无 base_url 的 fish apikey 号即原生号——密钥在 api_key，fish 无官方 Platform API。
func IsFishRelayAccount(account *Account) bool {
	if account == nil || account.Platform != PlatformFish || account.Type != AccountTypeAPIKey {
		return false
	}
	return strings.TrimSpace(account.GetCredential(fishCredentialRelayBaseURL)) != ""
}

func isFishRelayAccount(account *Account) bool {
	return IsFishRelayAccount(account)
}

// isFishTokenAccount 报告账号是否为 fish 原生号：有 api_key 且非中转。
func isFishTokenAccount(account *Account) bool {
	if account == nil || account.Platform != PlatformFish {
		return false
	}
	return !isFishRelayAccount(account) && strings.TrimSpace(account.GetCredential(fishCredentialToken)) != ""
}

// FishRequiresRelay 预判一次请求是否必须走中转（fish 原生无法承接）：
//   - 模型是 fish 公共名但尺寸无法就近规划（FishPlanSize 在 fitLimits 内
//     找不到 cover 档，如比例缺口过大的 1024x1536）——规划是纯函数，
//     选号前就能确定；
//   - mask / n>8 / output_format=webp / background（fish 产物恒 PNG 且
//     不支持这些参数）。
//
// 非 fish 模型名不在判定内：选号阶段按账号模型支持自然拦截。
func FishRequiresRelay(req *OpenAIImagesRequest, limits domain.FishFitLimits) bool {
	if req == nil {
		return false
	}
	if req.HasMask || req.N > fishRelayImageQuantityCap ||
		isImageOutputFormatWebP(req.OutputFormat) || strings.TrimSpace(req.Background) != "" {
		return true
	}
	if _, ok := domain.LookupFishModel(req.Model); ok {
		if _, err := domain.FishPlanSize(req.Size, limits); err != nil {
			return true
		}
	}
	return false
}

// FishRelaySelectionExclusions 构造「只走中转」阶段的选号排除集。
// failed 只拷贝，不就地改写，这样中转耗尽后原生号仍能被选到。
// requireRelay 时才并入 extraNativeIDs（选号循环里漏网的原生号）。
func FishRelaySelectionExclusions(failed map[int64]struct{}, requireRelay bool, extraNativeIDs map[int64]struct{}) map[int64]struct{} {
	out := make(map[int64]struct{}, len(failed)+len(extraNativeIDs))
	for id := range failed {
		out[id] = struct{}{}
	}
	if requireRelay {
		for id := range extraNativeIDs {
			out[id] = struct{}{}
		}
	}
	return out
}

// ExcludeFishNativeAccounts 把本组可调度的非中转 fish 账号并入 excluded，
// 避免「仅中转特性」阶段把换号预算浪费在不会成功的 token 原生号上。
func (s *GatewayService) ExcludeFishNativeAccounts(ctx context.Context, groupID *int64, excluded map[int64]struct{}) {
	if s == nil || excluded == nil {
		return
	}
	accounts, _, err := s.listSchedulableAccounts(ctx, groupID, PlatformFish, true)
	if err != nil {
		return
	}
	for i := range accounts {
		if !isFishRelayAccount(&accounts[i]) {
			excluded[accounts[i].ID] = struct{}{}
		}
	}
}

// fishAccountSupportsModel 判定 fish 账号能否承接 requestedModel：
// token 原生号与中转号都只接受 fish 公共名（gpt-image-2 等）——中转目标的
// 真实模型名（如 gpt-image-1）由渠道映射（channel mapping）在转发时改写。
func fishAccountSupportsModel(account *Account, requestedModel string) bool {
	if account == nil {
		return false
	}
	upstreamModel, matched := account.ResolveMappedModel(requestedModel)
	if !matched {
		return false
	}
	switch {
	case isFishRelayAccount(account):
		return domain.IsFishPublicModel(upstreamModel)
	case isFishTokenAccount(account):
		return domain.IsFishPublicModel(upstreamModel)
	default:
		return false
	}
}

// FishModelIDs 返回 fish 平台的对外模型名（展示顺序）。
func FishModelIDs() []string {
	return domain.FishModelNames()
}
