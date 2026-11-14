package service

// Ported from ranxi2001/sub2api@f895ea743 (backend/internal/service/quality_quota_filter.go).
// The source applied this only to template-generated rules; this fork has no
// templates, so every scheduled quality round of an OpenAI OAuth account uses it.

import (
	"context"
	"fmt"
	"math"
	"time"
)

// QualityQuotaAvailable reports known OpenAI OAuth quota availability. It never
// probes the provider and treats unknown observations as available.
func QualityQuotaAvailable(account *Account, now time.Time) bool {
	if account == nil || !account.IsOpenAIOAuth() {
		return true
	}
	if account.RateLimitResetAt != nil && now.Before(*account.RateLimitResetAt) {
		return false
	}
	if !openAICodexSnapshotIdentityTrusted(account) {
		return true
	}
	// Unlike a persisted cooldown, an undated/invalid observation cannot prove
	// exhaustion. Reuse scheduling's reset and stale-window handling below.
	for _, window := range []string{"5h", "7d"} {
		extra := qualityQuotaWindowObservation(account.Extra, window, now)
		used, known := resolveOpenAIQuotaUtilization(extra, window, now)
		if known && !math.IsNaN(used) && !math.IsInf(used, 0) && used >= 1 {
			return false
		}
	}
	return true
}

// Coalesced snapshots retain each field's original time.
func qualityQuotaWindowObservation(extra map[string]any, window string, now time.Time) map[string]any {
	prefix := "codex_" + window + "_"
	at, err := parseTime(fmt.Sprint(extra["codex_usage_updated_at"]))
	if err != nil || at.After(now) {
		return nil
	}
	out := map[string]any{prefix + "used_percent": extra[prefix+"used_percent"], "codex_usage_updated_at": at.Format(time.RFC3339Nano)}
	if raw, exists := extra[prefix+"reset_at"]; exists {
		reset, err := parseTime(fmt.Sprint(raw))
		if err != nil {
			return nil
		}
		out[prefix+"reset_at"] = reset.Format(time.RFC3339Nano)
	}
	return out
}

func (s *ScheduledTestRunnerService) qualityQuotaAvailable(ctx context.Context, plan *ScheduledTestPlan) (bool, error) {
	// Explicit "run now" requests keep their existing semantics.
	if plan.PelicanConfig == nil || plan.PelicanConfig.Quality == nil || plan.ManualQualityRun {
		return true, nil
	}
	if s.accountTestSvc == nil || s.accountTestSvc.accountRepo == nil {
		// Without an account lookup the quota is unknown, which never skips a round.
		return true, nil
	}
	account, err := s.accountTestSvc.accountRepo.GetByID(ctx, plan.AccountID)
	if err != nil {
		return false, err
	}
	return account != nil && QualityQuotaAvailable(account, time.Now()), nil
}
