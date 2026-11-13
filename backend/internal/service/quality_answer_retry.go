package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Retry only explicit HTTP rejections of quality answers. A timeout, broken
// stream or completed answer can already represent billable model work.
func retryableQualityAnswer(cfg *PelicanTestConfig, result *ScheduledTestResult) bool {
	if cfg == nil || cfg.Quality == nil || !isQualityAnswerQuestion(cfg.QuestionKind) ||
		result == nil || result.Status != "failed" || result.ResponseText != "" || result.QualityJudgment != nil {
		return false
	}
	for _, prefix := range []string{"API returned ", "Chat Completions API (/v1/chat/completions) returned "} {
		if rest, ok := strings.CutPrefix(result.ErrorMessage, prefix); ok {
			code, _, found := strings.Cut(rest, ": ")
			if !found || len(code) != 3 {
				return false
			}
			status, err := strconv.Atoi(code)
			if err != nil {
				return false
			}
			switch status {
			case 429, 500, 502, 503, 504:
				return true
			}
		}
	}
	return false
}

func (s *ScheduledTestRunnerService) runPelicanAnswer(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestResult, error) {
	started := time.Now()
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if batch := plan.PelicanConfig.QualityBatch; batch != nil && !time.Now().Before(batch.StartDeadline) {
			return nil, fmt.Errorf("quality batch start window closed")
		}
		// Reuse the prepared question and account: changing either would test a
		// different sample. Each call releases its own request resources.
		result, err := s.runPelican(ctx, plan.AccountID, plan.ModelID, plan.PelicanConfig)
		if err != nil {
			return nil, err
		}
		if result != nil && attempt > 0 {
			result.StartedAt = started
			result.FinishedAt = time.Now()
			result.LatencyMs = result.FinishedAt.Sub(started).Milliseconds()
		}
		// Three attempts total, with 1s and 2s backoff inside the round's
		// existing deadline. Exhaustion preserves the last failure as unknown.
		if attempt == 2 || !retryableQualityAnswer(plan.PelicanConfig, result) {
			return result, nil
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
