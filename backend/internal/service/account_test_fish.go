package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/fish"
	"github.com/gin-gonic/gin"
)

const (
	// fishTestTimeout 是连通性探测（档案 / 钱包 / 配额）的预算。
	fishTestTimeout = 30 * time.Second
	// fishTestImageTimeout 是 token 号真实出图的预算（提交 + 轮询）。
	fishTestImageTimeout = 3 * time.Minute
	fishTestImageSize    = "1024x1024"
	fishTestImageQuality = "low"
	fishTestPollInterval = 2 * time.Second
)

// testFishAccountConnection 验证 fish 账号。中转号走 OpenAI Images；token 号
// 先核对档案并回填身份，再按所选图像模型出一张图。
func (s *AccountTestService) testFishAccountConnection(c *gin.Context, account *Account, modelID, prompt string) error {
	if isFishRelayAccount(account) {
		testModel := strings.TrimSpace(modelID)
		if testModel == "" {
			if ids := FishModelIDs(); len(ids) > 0 {
				testModel = ids[0]
			}
		}
		return s.testOpenAIImageAPIKey(c, c.Request.Context(), account, account.GetMappedModel(testModel), resolveFishTestPrompt(prompt))
	}
	return s.testFishTokenConnection(c, account, "", modelID, prompt)
}

// resolveFishTestPrompt 补默认测试提示词（中转号连通性测试用）。
func resolveFishTestPrompt(prompt string) string {
	if strings.TrimSpace(prompt) != "" {
		return prompt
	}
	return "a red cube on a white background"
}

func (s *AccountTestService) testFishAccountConnectionWithBase(c *gin.Context, account *Account, apiBase string) error {
	return s.testFishTokenConnection(c, account, apiBase, "", "")
}

func (s *AccountTestService) testFishTokenConnection(c *gin.Context, account *Account, apiBase, modelID, prompt string) error {
	s.prepareGrokTestSSE(c)
	startModel := "fish /user/self"
	if mapped := strings.TrimSpace(modelID); mapped != "" {
		startModel = mapped
	}
	s.sendEvent(c, TestEvent{Type: "test_start", Model: startModel})

	token := strings.TrimSpace(account.GetCredential(fishCredentialToken))
	if token == "" {
		return s.sendErrorAndEnd(c, "fish account has no api_key — paste the fish.audio session value into credentials.api_key")
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), fishTestTimeout)
	defer cancel()

	// device_id 缺失时生成一个并随 team_id 一起回填（CAS：api_key 未变才写）
	deviceID := strings.TrimSpace(account.GetCredential(fishCredentialDeviceID))
	if deviceID == "" {
		deviceID = fish.NewDeviceID()
	}
	identity := fish.Identity{DeviceID: deviceID}

	client := fish.NewClient(fish.Options{ProxyURL: accountProxyURL(account), APIBase: apiBase})
	self, err := client.UserSelf(ctx, token, identity)
	if err != nil {
		return s.endFishTestError(c, account, err)
	}
	if self.Banned {
		return s.sendErrorAndEnd(c, "fish account is banned")
	}

	display := self.Email
	if display == "" {
		display = self.ID
	}
	s.sendEvent(c, TestEvent{Type: "status", Text: fmt.Sprintf("Account: %s", display)})

	wallet, err := client.QueryWallet(ctx, token, identity, self.ID)
	if err == nil && wallet != nil {
		s.sendEvent(c, TestEvent{Type: "status",
			Text: fmt.Sprintf("Credits: %d / %d (%s)", wallet.Balance, wallet.Total, wallet.Type)})
	}
	var entitlements *fish.Entitlements
	if ent, err := client.QueryEntitlements(ctx, token, identity); err == nil && ent != nil {
		entitlements = ent
		s.sendEvent(c, TestEvent{Type: "status",
			Text: fmt.Sprintf("Plan: %s, max images per generation: %d", ent.Plan, ent.MaxImagesPerGeneration)})
	}

	if account.ID > 0 {
		credentials := shallowCopyMap(account.Credentials)
		credentials[fishCredentialToken] = token
		credentials[fishCredentialTeamID] = self.ID
		credentials[fishCredentialDeviceID] = deviceID
		if entitlements != nil && entitlements.MaxImagesPerGeneration >= 1 {
			credentials[fishCredentialMaxPerGen] = entitlements.MaxImagesPerGeneration
		}
		if entitlements != nil && strings.TrimSpace(entitlements.Plan) != "" {
			credentials[fishCredentialPlan] = strings.TrimSpace(entitlements.Plan)
		}
		// 内存态先更新（repo 缺失时 persistAccountCredentials 直接跳过），
		// 再走标准持久化。
		account.Credentials = credentials
		if updateErr := persistAccountCredentials(ctx, s.accountRepo, account, credentials); updateErr != nil {
			// 回填失败不阻断测试：身份头本就可省
			s.sendEvent(c, TestEvent{Type: "status", Text: "Note: failed to persist team_id/device_id (identity headers stay optional)"})
		}
	}

	mappedModel := strings.TrimSpace(modelID)
	if mappedModel != "" {
		mappedModel = account.GetMappedModel(mappedModel)
		if !domain.IsFishPublicModel(mappedModel) {
			return s.sendErrorAndEnd(c, fmt.Sprintf("fish model %q is not available on this account", modelID))
		}
		if err := s.testFishTokenImage(c, account, client, token, identity, mappedModel, prompt); err != nil {
			return err
		}
	}

	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

// testFishTokenImage 用最低档 1024x1024 出一张图，并把产物 URL 推给测试窗。
func (s *AccountTestService) testFishTokenImage(c *gin.Context, account *Account, client *fish.Client, token string, identity fish.Identity, modelID, prompt string) error {
	ctx, cancel := context.WithTimeout(c.Request.Context(), fishTestImageTimeout)
	defer cancel()

	prompt = resolveFishTestPrompt(prompt)
	s.sendEvent(c, TestEvent{Type: "status", Text: "Pricing image..."})
	price, err := client.Price(ctx, token, identity, fish.PriceParams{
		Model:     modelID,
		Variant:   fish.VariantTextToImage,
		ImageSize: fishTestImageSize,
		Quality:   fishTestImageQuality,
		Count:     1,
	})
	if err != nil {
		return s.endFishTestError(c, account, err)
	}
	if price == nil {
		return s.sendErrorAndEnd(c, "fish price response was empty")
	}

	s.sendEvent(c, TestEvent{Type: "status", Text: fmt.Sprintf("Generating image (%d credits)...", price.Credits)})
	group, err := client.SubmitGeneration(ctx, token, identity, fish.GenerateRequest{
		Model:           modelID,
		Variant:         fish.VariantTextToImage,
		Prompt:          prompt,
		Count:           1,
		ImageSize:       fishTestImageSize,
		Quality:         fishTestImageQuality,
		ExpectedCredits: price.Credits,
		PricingVersion:  price.PricingVersion,
	})
	if err != nil {
		return s.endFishTestError(c, account, err)
	}
	if group == nil || strings.TrimSpace(group.ID) == "" {
		return s.sendErrorAndEnd(c, "fish generate returned no task group")
	}

	ticker := time.NewTicker(fishTestPollInterval)
	defer ticker.Stop()
	for group.Status != "done" && group.Status != "failed" {
		select {
		case <-ctx.Done():
			return s.sendErrorAndEnd(c, "fish image test timed out")
		case <-ticker.C:
		}
		next, pollErr := client.PollTaskGroup(ctx, token, identity, group.ID)
		if pollErr != nil {
			if ctx.Err() != nil {
				return s.sendErrorAndEnd(c, "fish image test timed out")
			}
			continue
		}
		if next != nil {
			group = next
		}
	}
	if group.Status == "failed" {
		return s.sendErrorAndEnd(c, "fish image generation failed")
	}
	for _, result := range group.Results {
		if result.MediaItem != nil && strings.TrimSpace(result.MediaItem.URL) != "" {
			s.sendEvent(c, TestEvent{Type: "image", ImageURL: result.MediaItem.URL, MimeType: "image/png"})
			return nil
		}
	}
	return s.sendErrorAndEnd(c, "fish image generation finished without a media URL")
}

// endFishTestError 结束连通性测试并上报错误。撞上游 429 时按请求路径同口径
// 落限流冷却（free 号到下一个 UTC 零点——当天基本废了，短冷却回锅会虚增
// 可用账号数、挡住自动补号；其余 5 分钟），否则管理员测出 rate limited 后
// 紧接着的请求仍会选到刚被上游限流的号。
func (s *AccountTestService) endFishTestError(c *gin.Context, account *Account, err error) error {
	var rateLimited *fish.RateLimitedError
	if errors.As(err, &rateLimited) && s != nil && s.accountRepo != nil && account != nil && account.ID > 0 && isFishTokenAccount(account) {
		until := time.Now().Add(fishRateLimitCooldown)
		if fishAccountPlanIsFree(account) {
			until = nextUTCMidnight(time.Now())
		}
		writeFishCooldown(c.Request.Context(), s.accountRepo, account.ID, until, fishRateLimitCooldownReason)
	}
	return s.sendErrorAndEnd(c, formatFishTestError(err))
}

// formatFishTestError 把 fish 客户端错误转成面向管理员的简短文案。
func formatFishTestError(err error) string {
	var authErr *fish.AuthError
	if errors.As(err, &authErr) {
		return "fish api_key was rejected — re-export a fresh session value into credentials.api_key"
	}
	var rateLimited *fish.RateLimitedError
	if errors.As(err, &rateLimited) {
		return "fish upstream rate limited the test — retry in a moment"
	}
	return err.Error()
}
