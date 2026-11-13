package service

// Ported from ranxi2001/sub2api@f895ea743 (backend/internal/service/quality_supported_models.go).

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/typesafe"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func qualityUnsupportedModelKey(model string) string {
	return fmt.Sprintf("quality_unsupported_model_%x", sha256.Sum256([]byte(model)))
}

// Discovery is shared with the account test picker. An unavailable catalog is
// an error, never evidence that the account does not support a model.
func (s *ScheduledTestService) accountQualityModels(ctx context.Context, id int64) ([]string, error) {
	account, err := s.accountTests.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrAccountNotFound
	}
	var models []string
	if account.IsOpenAI() {
		catalog, err := s.accountTests.FetchOpenAIAccountModels(ctx, account)
		if err != nil {
			return nil, err
		}
		for _, model := range catalog {
			models = append(models, model.ID)
		}
	} else if account.IsTypeSafe() {
		models = []string{typesafe.JevLatestModel}
	} else if mapping := account.GetModelMapping(); len(mapping) > 0 {
		for model := range mapping {
			models = append(models, model)
		}
	} else {
		switch account.Platform {
		case PlatformGemini:
			catalog := geminicli.DefaultModels
			if account.IsGeminiGoogleOne() {
				catalog = geminicli.GoogleOneModels
			}
			for _, model := range catalog {
				models = append(models, model.ID)
			}
		case PlatformGrok:
			for _, model := range xai.DefaultModels() {
				models = append(models, model.ID)
			}
		case PlatformAntigravity:
			for _, model := range antigravity.DefaultModels() {
				models = append(models, model.ID)
			}
		case PlatformDeepseek:
			models = defaultModelsListCandidateIDs(PlatformDeepseek)
		default:
			for _, model := range claude.DefaultModels {
				models = append(models, model.ID)
			}
		}
	}
	supported := models[:0]
	for _, model := range models {
		if account.Extra[qualityUnsupportedModelKey(model)] == nil {
			supported = append(supported, model)
		}
	}
	return supported, nil
}

func qualityPlanModels(plan *ScheduledTestPlan) []string {
	if plan.PelicanConfig != nil && len(plan.PelicanConfig.ModelIDs) > 0 {
		return plan.PelicanConfig.ModelIDs
	}
	return []string{plan.ModelID}
}

func qualityCatalogSupportsModel(catalog []string, model string) bool {
	for _, pattern := range catalog {
		if pattern == model || strings.Contains(pattern, "*") && matchWildcard(pattern, model) {
			return true
		}
	}
	return false
}

// supportedQualityModels projects advisory catalog/configured candidates. A
// missing candidate alone is not proof of upstream capability; production
// create/update, template and execution decisions use eligibleQualityModels.
func (s *ScheduledTestService) supportedQualityModels(ctx context.Context, plan *ScheduledTestPlan) ([]string, []string, error) {
	requested := qualityPlanModels(plan)
	if plan.PelicanConfig == nil || plan.PelicanConfig.Quality == nil || s.qualityModels == nil {
		return requested, nil, nil
	}
	var catalog []string
	var blocked map[string]any
	if s.accountTests != nil {
		account, err := s.accountTests.accountRepo.GetByID(ctx, plan.AccountID)
		if err != nil {
			return nil, nil, err
		}
		if account != nil {
			blocked = account.Extra
			// Native candy tests can exercise explicitly configured public model
			// names even when an upstream /models catalog omits them. This applies
			// to every account type; it does not claim upstream availability.
			if isQualityAnswerQuestion(plan.PelicanConfig.QuestionKind) &&
				!account.IsExcelBPSEnabled() && !account.IsOpenAIPassthroughEnabled() {
				for model := range account.GetModelMapping() {
					catalog = append(catalog, model)
				}
			}
		}
	}
	for _, model := range requested {
		if !qualityCatalogSupportsModel(catalog, model) {
			discovered, err := s.qualityModels(ctx, plan.AccountID)
			if err != nil {
				return nil, nil, fmt.Errorf("无法读取账号 #%d 的模型列表：%w", plan.AccountID, err)
			}
			catalog = append(catalog, discovered...)
			break
		}
	}
	var supported, unsupported []string
	for _, model := range requested {
		found := qualityCatalogSupportsModel(catalog, model)
		if blocked[qualityUnsupportedModelKey(model)] != nil {
			found = false
		}
		if found {
			supported = append(supported, model)
		} else {
			unsupported = append(unsupported, model)
		}
	}
	return supported, unsupported, nil
}

func qualityAccountAllowsModel(account *Account, model string) bool {
	return (!account.IsTypeSafe() || model == typesafe.JevLatestModel) && account.IsModelSupported(model)
}

// eligibleQualityModels separates routing restrictions and observed rejections
// from incomplete discovery at the actual business boundary.
func (s *ScheduledTestService) eligibleQualityModels(ctx context.Context, plan *ScheduledTestPlan) ([]string, []string, error) {
	if plan.PelicanConfig == nil || plan.PelicanConfig.Quality == nil || s.accountTests == nil {
		return s.supportedQualityModels(ctx, plan)
	}
	account, err := s.accountTests.accountRepo.GetByID(ctx, plan.AccountID)
	if err != nil {
		return nil, nil, err
	}
	if account == nil {
		return nil, nil, ErrAccountNotFound
	}
	var allowed, blocked []string
	for _, model := range qualityPlanModels(plan) {
		if !qualityAccountAllowsModel(account, model) || account.Extra[qualityUnsupportedModelKey(model)] != nil || account.Extra[qualityUnsupportedModelKey(account.GetMappedModel(model))] != nil {
			blocked = append(blocked, model)
		} else {
			allowed = append(allowed, model)
		}
	}
	if len(allowed) == 0 {
		return nil, blocked, nil
	}
	manual := isQualityAnswerQuestion(plan.PelicanConfig.QuestionKind) && !account.IsExcelBPSEnabled() && !account.IsOpenAIPassthroughEnabled()
	if manual {
		configured := true
		for _, model := range allowed {
			if !mappingSupportsRequestedModel(account.GetModelMapping(), model) {
				configured = false
				break
			}
		}
		if configured {
			return allowed, blocked, nil
		}
	}
	if s.qualityModels == nil {
		return nil, nil, infraerrors.ServiceUnavailable("QUALITY_MODEL_CATALOG_UNKNOWN", "模型目录尚未就绪，请稍后刷新")
	}
	candidate := *plan
	candidate.PelicanConfig = cloneQualityPelicanConfig(plan.PelicanConfig)
	candidate.PelicanConfig.ModelIDs = allowed
	candidate.ModelID = allowed[0]
	found, missing, err := s.supportedQualityModels(ctx, &candidate)
	if err != nil {
		return nil, nil, err
	}
	if manual {
		return allowed, blocked, nil
	}
	if len(missing) > 0 {
		return nil, nil, infraerrors.ServiceUnavailable("QUALITY_MODEL_CATALOG_UNKNOWN", "模型目录尚未确认该检测模型，请刷新模型列表或使用允许手动选择的原生回答检测")
	}
	return found, blocked, nil
}

func (s *ScheduledTestService) validateQualityAccountModels(ctx context.Context, plan *ScheduledTestPlan) error {
	if plan.PelicanConfig != nil && plan.PelicanConfig.Quality != nil && s.accountTests != nil {
		account, err := s.accountTests.accountRepo.GetByID(ctx, plan.AccountID)
		if err != nil {
			return err
		}
		if account == nil {
			return ErrAccountNotFound
		}
		for _, model := range qualityPlanModels(plan) {
			if !qualityAccountAllowsModel(account, model) {
				return infraerrors.BadRequest("QUALITY_MODEL_NOT_ALLOWED", "账号检测策略不允许所选模型，请核对模型映射、账号权限和检测通道")
			}
		}
	}

	_, unsupported, err := s.eligibleQualityModels(ctx, plan)
	if err != nil {
		return err
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("账号 #%d 不支持检测模型：%s", plan.AccountID, strings.Join(unsupported, ", "))
	}
	return nil
}

// Match explicit model errors only; invalid tools, transient failures and
// authorization errors must never permanently suppress a model.
var qualityAuthFailureMessage = regexp.MustCompile(`(?i)(?:\b(?:API returned|HTTP(?: status)?|status(?: code)?)\s*[:=]?\s*(?:401|403)\b|\((?:401|403)\)|\b(?:token_revoked|token_invalidated|unauthorized|invalid_token)\b)`)

var qualityUnsupportedModelMessage = regexp.MustCompile(`(?i)\bmodel\b.{0,160}\b(?:not supported|not found|does not exist)\b`)

func qualityModelUnsupported(message string) bool {
	lower := strings.ToLower(message)
	// Authentication/permission failure is never evidence of model capability,
	// even if an upstream error happens to mention a model rejection code.
	if strings.Contains(lower, "not supported by any configured account") || strings.Contains(lower, "permission denied") || strings.Contains(lower, "openai_codex_models_credentials_invalid") || qualityAuthFailureMessage.MatchString(lower) {
		return false
	}
	for _, code := range []string{"model_not_found", "model_not_supported", "unsupported_model", "model_unsupported"} {
		if strings.Contains(lower, code) {
			return true
		}
	}
	if !strings.Contains(lower, "model") && !strings.Contains(lower, "模型") {
		return false
	}
	return qualityUnsupportedModelMessage.MatchString(lower) || strings.Contains(lower, "model is not supported") || strings.Contains(lower, "model not supported") ||
		strings.Contains(lower, "does not support this model") || strings.Contains(lower, "not supported with") ||
		strings.Contains(lower, "not supported by any configured account") || strings.Contains(lower, "model") && strings.Contains(lower, "does not exist") ||
		strings.Contains(lower, "不支持该模型") || strings.Contains(lower, "模型不支持") || strings.Contains(lower, "模型不存在")
}

func (s *ScheduledTestService) rememberUnsupportedQualityModel(ctx context.Context, id int64, model string) error {
	if s.accountTests == nil {
		return nil
	}
	// Independent keys use the native atomic Extra merge, so concurrent rules
	// cannot overwrite each other's observations or unrelated account settings.
	return s.accountTests.accountRepo.UpdateExtra(ctx, id, map[string]any{qualityUnsupportedModelKey(model): model})
}

// Every plan gets its own copy: validation writes the model into the config.
func cloneQualityPelicanConfig(cfg *PelicanTestConfig) *PelicanTestConfig {
	if cfg == nil {
		return nil
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil
	}
	var out PelicanTestConfig
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return &out
}
