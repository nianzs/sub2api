package service

// Ported from ranxi2001/sub2api@f895ea743 (backend/internal/service/pelican_test_service.go).
// Only the OpenAI answer path is wired in this fork; Claude payloads, BPS
// channels and OAuth 401 evidence are not ported.

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
)

type pelicanTestContextKey struct{}

// qualityProbeContextKey marks trusted internal quality probes.
type qualityProbeContextKey struct{}

type pelicanTestOptions struct {
	qualityBatch    *QualityBatchEvidence
	prompt          string
	reasoningEffort string
}

func withPelicanTestOptions(ctx context.Context, options pelicanTestOptions) context.Context {
	ctx = context.WithValue(ctx, qualityProbeContextKey{}, true)
	return context.WithValue(ctx, pelicanTestContextKey{}, options)
}

func pelicanTestOptionsFromContext(ctx context.Context) (pelicanTestOptions, bool) {
	options, ok := ctx.Value(pelicanTestContextKey{}).(pelicanTestOptions)
	return options, ok
}

// Record the chosen test path and mapped target, never credentials or URLs.
func recordQualityBatchRoute(c *gin.Context, account *Account, model, route string) {
	if c == nil || c.Request == nil || account == nil {
		return
	}
	options, ok := pelicanTestOptionsFromContext(c.Request.Context())
	if ok && options.qualityBatch != nil {
		options.qualityBatch.Route = route
		options.qualityBatch.MappedModel = account.GetMappedModel(model)
	}
}

// qualityProbeSupported reports whether this fork can deliver the quality
// prompt and reasoning effort to the account. Only the native OpenAI answer
// path carries them; other platforms would silently send the connectivity
// probe and must never be graded.
func qualityProbeSupported(account *Account) bool {
	return account != nil && account.Platform == PlatformOpenAI && !account.IsBPSOAuth()
}

// TestPelicanAccountConnection runs an account test with the quality prompt and
// reasoning effort. The existing /test endpoint keeps its historical payload.
func (s *AccountTestService) TestPelicanAccountConnection(c *gin.Context, accountID int64, modelID, prompt, reasoningEffort string) error {
	options, _ := pelicanTestOptionsFromContext(c.Request.Context())
	options.prompt = strings.TrimSpace(prompt)
	options.reasoningEffort = normalizePelicanReasoningEffort(reasoningEffort)
	ctx := withPelicanTestOptions(c.Request.Context(), options)
	c.Request = c.Request.WithContext(ctx)
	return s.TestAccountConnection(c, accountID, modelID, options.prompt, AccountTestModeDefault)
}

func normalizePelicanReasoningEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func createPelicanOpenAIPayload(modelID string, isOAuth bool, prompt, reasoningEffort string) map[string]any {
	payload := createOpenAITestPayload(modelID, isOAuth)
	input, ok := payload["input"].([]map[string]any)
	if ok && len(input) > 0 {
		content, ok := input[0]["content"].([]map[string]any)
		if ok && len(content) > 0 {
			content[0]["text"] = promptOrDefault(prompt)
		}
	}
	if effort := normalizePelicanReasoningEffort(reasoningEffort); effort != "" {
		payload["reasoning"] = map[string]any{"effort": effort}
	}
	return payload
}
