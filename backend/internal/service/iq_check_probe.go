package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/gin-gonic/gin"
)

// iqCheckReasoningEffort is fixed: the check looks for accounts that no longer
// reason at the requested effort, so it always asks for a high one.
const iqCheckReasoningEffort = "high"

type iqCheckProbeContextKey struct{}

type iqCheckProbeOptions struct {
	prompt          string
	reasoningEffort string
}

func iqCheckProbeOptionsFromContext(ctx context.Context) (iqCheckProbeOptions, bool) {
	options, ok := ctx.Value(iqCheckProbeContextKey{}).(iqCheckProbeOptions)
	return options, ok
}

// applyIQCheckResponsesPayload replaces the connectivity probe's "hi" with the
// IQ check question and requests the check's reasoning effort.
func applyIQCheckResponsesPayload(ctx context.Context, payload map[string]any) {
	options, ok := iqCheckProbeOptionsFromContext(ctx)
	if !ok {
		return
	}
	if input, ok := payload["input"].([]map[string]any); ok && len(input) > 0 {
		if content, ok := input[0]["content"].([]map[string]any); ok && len(content) > 0 && options.prompt != "" {
			content[0]["text"] = options.prompt
		}
	}
	if options.reasoningEffort != "" {
		payload["reasoning"] = map[string]any{"effort": options.reasoningEffort}
	}
}

// RunIQCheckProbe sends one IQ check question through the account test path
// and returns the model's answer text. Upstream errors are returned verbatim.
func (s *AccountTestService) RunIQCheckProbe(ctx context.Context, accountID int64, modelID, prompt string) (string, error) {
	ctx = context.WithValue(ctx, iqCheckProbeContextKey{}, iqCheckProbeOptions{prompt: prompt, reasoningEffort: iqCheckReasoningEffort})
	w := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(w)
	ginCtx.Request = (&http.Request{Header: make(http.Header)}).WithContext(ctx)
	ginCtx.Set(accountTestBackgroundKey, true)

	testErr := s.TestAccountConnection(ginCtx, accountID, modelID, prompt, AccountTestModeDefault)
	text, errMsg := parseTestSSEOutput(w.Body.String())
	if errMsg == "" && testErr != nil {
		errMsg = testErr.Error()
	}
	if errMsg != "" {
		return "", errors.New(errMsg)
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("model returned an empty answer")
	}
	return text, nil
}
