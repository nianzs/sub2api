package service

// Ported from ranxi2001/sub2api@f895ea743 (backend/internal/service/account_quality.go).
// Trimmed to the observe_only / disable_scheduling / remove_groups actions; model
// isolation, BPS, 2FA admission, OAuth 401 cleanup and the state probe are not ported.

import (
	"context"
	"fmt"
	"strings"
)

const QualityActionObserveOnly = "observe_only"

// QualityModels is the single source of truth for the models a policy manages.
func QualityModels(plan *ScheduledTestPlan) []string {
	return qualityPlanModels(plan)
}

// QualityPolicy is opt-in. Legacy connectivity tests never modify membership.
type QualityPolicy struct {
	Judge             *QualityJudgeConfig `json:"judge,omitempty"`
	ExpectedAnswer    string              `json:"expected_answer"`
	Action            string              `json:"action"`
	RemoveGroupIDs    []int64             `json:"remove_group_ids"`
	RetestRestMinutes int                 `json:"retest_rest_minutes,omitempty"`
	AutoRestore       bool                `json:"auto_restore"`
}

func validateQualityPolicy(plan *ScheduledTestPlan) error {
	q := plan.PelicanConfig.Quality
	if q == nil {
		return nil
	}
	if q.RetestRestMinutes < 0 || q.RetestRestMinutes > 10080 {
		return fmt.Errorf("quality retest rest must be 0-10080 minutes")
	}
	if !q.UsesRetestRest() {
		q.RetestRestMinutes = 0
	}
	if j := q.Judge; j != nil {
		if strings.TrimSpace(j.Prompt) == "" || len(j.Prompt) > 16000 {
			return fmt.Errorf("grading prompt must be 1-16000 bytes")
		}
		switch j.Kind {
		case "", "llm":
			if j.GroupID <= 0 || strings.TrimSpace(j.ModelID) == "" || len(j.ModelID) > 100 {
				return fmt.Errorf("judge group, model and grading prompt are required (100/16000 byte limits)")
			}
		default:
			return fmt.Errorf("unsupported quality judge kind")
		}
	}
	if plan.AutoRecover {
		return fmt.Errorf("quality plans use auto_restore, not connectivity auto_recover")
	}
	if !isQualityAnswerQuestion(plan.PelicanConfig.QuestionKind) {
		return fmt.Errorf("quality plans require a text answer question")
	}
	// 随机题的参考答案由执行器生成，其他答题规则必须配置答案。
	if plan.PelicanConfig.QuestionKind != QualityRandomQuestionKind {
		if strings.TrimSpace(q.ExpectedAnswer) == "" || len(q.ExpectedAnswer) > 4000 {
			return fmt.Errorf("expected answer must be 1–4000 bytes")
		}
	} else if len(q.ExpectedAnswer) > 4000 {
		return fmt.Errorf("expected answer must be 1–4000 bytes")
	}
	if q.Action != "remove_groups" && q.Action != "disable_scheduling" && q.Action != QualityActionObserveOnly {
		return fmt.Errorf("invalid quality action")
	}
	if q.Action == QualityActionObserveOnly {
		q.AutoRestore = false
		q.RemoveGroupIDs = nil
	}
	if q.Action == "remove_groups" && len(q.RemoveGroupIDs) == 0 {
		return fmt.Errorf("select at least one group to remove")
	}
	if len(q.RemoveGroupIDs) > 100 {
		return fmt.Errorf("select at most 100 groups")
	}
	seen := map[int64]bool{}
	for _, id := range q.RemoveGroupIDs {
		if id <= 0 || seen[id] {
			return fmt.Errorf("invalid or duplicate group ID")
		}
		seen[id] = true
	}
	return nil
}

// Observation never quarantines accounts, so it never rests.
func (q *QualityPolicy) UsesRetestRest() bool {
	return q.Action == "remove_groups" || q.Action == "disable_scheduling"
}

// One completed wrong answer quarantines; restoration requires every probe to pass.
// Transport errors alone are inconclusive, never evidence of degradation.
// Multi-model rules pass only when every selected model/sample passed.
func qualityPlanModelOutcomes(plan *ScheduledTestPlan, results []*ScheduledTestResult) map[string]string {
	return qualityModelOutcomes(results, QualityModels(plan))
}

func qualityModelOutcomes(results []*ScheduledTestResult, targets []string) map[string]string {
	outcomes := make(map[string]string, len(targets))
	for _, model := range targets {
		var samples []*ScheduledTestResult
		for _, result := range results {
			if result != nil && result.PelicanConfig != nil && result.PelicanConfig.ModelID == model {
				samples = append(samples, result)
			}
		}
		if len(samples) == 0 || !qualityRoundHasResults(samples) {
			outcomes[model] = "skipped"
		} else {
			// A partially skipped model did not complete every configured sample.
			// Do not let its successful sample release an existing restriction.
			outcomes[model] = qualityOutcome(samples)
		}
	}
	return outcomes
}

func qualityRoundHasResults(results []*ScheduledTestResult) bool {
	for _, result := range results {
		if result == nil || result.Status != "skipped" {
			return true
		}
	}
	return false
}

func qualityRoundOutcome(results []*ScheduledTestResult) string {
	effective := make([]*ScheduledTestResult, 0, len(results))
	for _, result := range results {
		if result == nil || result.Status != "skipped" {
			effective = append(effective, result)
		}
	}
	if len(effective) == 0 {
		return "inconclusive"
	}
	// Unknown grading or transport failure cannot become a wrong answer merely
	// because the rule tests multiple models. Recovery still requires all passes.
	return qualityOutcome(effective)
}

func qualityOutcome(results []*ScheduledTestResult) string {
	allPassed := len(results) > 0
	for _, r := range results {
		if r != nil && r.QualityJudgment != nil && r.QualityJudgment.Verdict == "incorrect" && r.Status == "failed" &&
			r.ErrorMessage == "answer_mismatch" {
			return "failed"
		}
		if r == nil || r.Status != "success" || r.QualityJudgment == nil || r.QualityJudgment.Verdict != "correct" {
			allPassed = false
		}
	}
	if allPassed {
		return "passed"
	}
	return "inconclusive"
}

func (s *ScheduledTestService) ListQualityPlans(ctx context.Context) ([]*ScheduledTestPlan, error) {
	return s.planRepo.ListQualityPlans(ctx)
}
func (s *ScheduledTestService) TriggerQuality(ctx context.Context, id int64) error {
	return s.planRepo.TriggerQuality(ctx, id)
}

func (s *ScheduledTestService) ListQualityHistory(ctx context.Context, beforeID int64) (*QualityHistoryPage, error) {
	items, err := s.resultRepo.ListQualityHistory(ctx, beforeID, 101)
	if err != nil {
		return nil, err
	}
	// Results are persisted individually after all model requests have ended.
	// During those writes (or after a failed write), never label a partial round
	// as passing simply because its first saved sample passed.
	for _, item := range items {
		expected := item.TotalCount
		if cfg := item.PelicanConfig; cfg != nil && len(cfg.ModelIDs) > 1 {
			if planned := len(cfg.ModelIDs) * cfg.ParallelCount; planned > expected {
				expected = planned
			}
		}
		item.TotalCount = expected - item.SkippedCount
		if item.TotalCount <= 0 {
			item.TotalCount = 0
			item.Status = "skipped"
		} else if item.PassedCount == item.TotalCount {
			item.Status = "success"
		} else {
			item.Status = "failed"
		}
	}
	page := &QualityHistoryPage{Items: items}
	if len(items) > 100 {
		page.Items = items[:100]
		page.NextCursor = items[99].ID
	}
	return page, nil
}
