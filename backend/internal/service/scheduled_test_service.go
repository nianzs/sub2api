package service

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
)

var scheduledTestCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ScheduledTestService provides CRUD operations for scheduled test plans and results.
// Quality-rule validation is ported from ranxi2001/sub2api@f895ea743.
type ScheduledTestService struct {
	planRepo      ScheduledTestPlanRepository
	resultRepo    ScheduledTestResultRepository
	accountTests  *AccountTestService
	qualityModels func(context.Context, int64) ([]string, error)
}

// NewScheduledTestService creates a new ScheduledTestService.
func NewScheduledTestService(
	planRepo ScheduledTestPlanRepository,
	resultRepo ScheduledTestResultRepository,
) *ScheduledTestService {
	return &ScheduledTestService{
		planRepo:   planRepo,
		resultRepo: resultRepo,
	}
}

// SetAccountTests enables quality-rule model discovery and validation.
func (s *ScheduledTestService) SetAccountTests(tests *AccountTestService) {
	if s == nil || tests == nil {
		return
	}
	s.accountTests = tests
	s.qualityModels = s.accountQualityModels
}

// CreatePlan validates the cron expression, computes next_run_at, and persists the plan.
func (s *ScheduledTestService) CreatePlan(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	nextRun, err := nextPlanRun(plan, time.Now())
	if err != nil {
		return nil, fmt.Errorf("invalid test schedule: %w", err)
	}
	if err := s.validateQualityAccountModels(ctx, plan); err != nil {
		return nil, err
	}
	plan.NextRunAt = &nextRun

	if plan.MaxResults <= 0 {
		plan.MaxResults = 50
	}

	return s.planRepo.Create(ctx, plan)
}

// GetPlan retrieves a plan by ID.
func (s *ScheduledTestService) GetPlan(ctx context.Context, id int64) (*ScheduledTestPlan, error) {
	return s.planRepo.GetByID(ctx, id)
}

// ListPlansByAccount returns all plans for a given account.
func (s *ScheduledTestService) ListPlansByAccount(ctx context.Context, accountID int64) ([]*ScheduledTestPlan, error) {
	return s.planRepo.ListByAccountID(ctx, accountID)
}

// UpdatePlan validates cron and updates the plan.
func (s *ScheduledTestService) UpdatePlan(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	nextRun, err := nextPlanRun(plan, time.Now())
	if err != nil {
		return nil, fmt.Errorf("invalid test schedule: %w", err)
	}
	if plan.Enabled {
		if err := s.validateQualityAccountModels(ctx, plan); err != nil {
			return nil, err
		}
	}
	plan.NextRunAt = &nextRun

	return s.planRepo.Update(ctx, plan)
}

// DeletePlan removes a plan and its results (via CASCADE).
func (s *ScheduledTestService) DeletePlan(ctx context.Context, id int64) error {
	return s.planRepo.Delete(ctx, id)
}

// ListResults returns the most recent results for a plan.
func (s *ScheduledTestService) ListResults(ctx context.Context, planID int64, limit int, includeContent ...bool) ([]*ScheduledTestResult, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.resultRepo.ListByPlanID(ctx, planID, limit, includeContent...)
}

// SaveResult inserts a result and prunes old entries beyond maxResults.
func (s *ScheduledTestService) SaveResult(ctx context.Context, planID int64, maxResults int, result *ScheduledTestResult) error {
	result.PlanID = planID
	if _, err := s.resultRepo.Create(ctx, result); err != nil {
		return err
	}
	return s.resultRepo.PruneOldResults(ctx, planID, maxResults)
}

func computeNextRun(cronExpr string, from time.Time) (time.Time, error) {
	sched, err := scheduledTestCronParser.Parse(cronExpr)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(from), nil
}

func nextPlanRun(plan *ScheduledTestPlan, now time.Time) (time.Time, error) {
	if cfg := plan.PelicanConfig; cfg != nil {
		// This fork only runs quality rules through pelican_config; the
		// HTML/SVG Pelican tests of the source fork are not ported.
		if cfg.Quality == nil {
			return time.Time{}, fmt.Errorf("pelican_config requires a quality rule")
		}
		cfg.RuleName = strings.TrimSpace(cfg.RuleName)
		if utf8.RuneCountInString(cfg.RuleName) > 100 {
			return time.Time{}, fmt.Errorf("rule name must be at most 100 characters")
		}
		cfg.QualityRestoreDiagnostic = nil
		cfg.QualityModelOutcomes = nil
		cfg.RandomQuestion = nil
		cfg.QualityBatch = nil
		cfg.TriggerSource = ""
		cfg.ActualPrompt = ""
		if cfg.QuestionKind == QualityRandomQuestionKind {
			// Generated data belongs to results, never to reusable rule JSON.
			cfg.Prompt = ""
			cfg.Quality.ExpectedAnswer = ""
		}
		if len(cfg.ModelIDs) > 0 {
			if len(cfg.ModelIDs) > 50 {
				return time.Time{}, fmt.Errorf("select at most 50 quality models")
			}
			models := make([]string, 0, len(cfg.ModelIDs))
			seen := map[string]bool{}
			for _, raw := range cfg.ModelIDs {
				model := strings.TrimSpace(raw)
				if model == "" || len(model) > 100 {
					return time.Time{}, fmt.Errorf("quality model must be 1–100 bytes")
				}
				if !seen[model] {
					models = append(models, model)
					seen[model] = true
				}
			}
			if len(models)*cfg.ParallelCount > 100 {
				return time.Time{}, fmt.Errorf("quality round supports at most 100 samples")
			}
			cfg.ModelIDs = models
			plan.ModelID = models[0]
		}
		if cfg.QuestionKind == QualityRandomQuestionKind {
			if strings.TrimSpace(plan.ModelID) == "" || len(plan.ModelID) > 100 {
				return time.Time{}, fmt.Errorf("probe model is required (maximum 100 bytes)")
			}
		} else if strings.TrimSpace(cfg.Prompt) == "" || len(cfg.Prompt) > 32000 || strings.TrimSpace(plan.ModelID) == "" || len(plan.ModelID) > 100 {
			return time.Time{}, fmt.Errorf("question prompt and model are required (maximum 32000/100 bytes)")
		}
		if err := validateQualityPolicy(plan); err != nil {
			return time.Time{}, err
		}
		if cfg.ParallelCount < 1 || cfg.ParallelCount > 8 {
			return time.Time{}, fmt.Errorf("parallel count must be 1–8")
		}
		if normalizePelicanReasoningEffort(cfg.ReasoningEffort) == "" {
			return time.Time{}, fmt.Errorf("invalid reasoning effort")
		}
		if plan.MaxResults == 0 {
			plan.MaxResults = 100
		}
		if plan.MaxResults < 1 || plan.MaxResults > 200 {
			return time.Time{}, fmt.Errorf("quality history retention must be 1–200 results")
		}
		if len(cfg.ModelIDs) > 1 && plan.MaxResults < len(cfg.ModelIDs)*cfg.ParallelCount {
			return time.Time{}, fmt.Errorf("history retention must hold every sample in a quality round")
		}
		cfg.ModelID = plan.ModelID
	}
	return computeNextRun(plan.CronExpression, now)
}

func (s *ScheduledTestService) GetResult(ctx context.Context, planID, resultID int64) (*ScheduledTestResult, error) {
	return s.resultRepo.GetResult(ctx, planID, resultID)
}
