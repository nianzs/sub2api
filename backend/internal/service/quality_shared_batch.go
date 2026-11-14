package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// Questions are fixed once per minute across all primary replicas. Starting
// outside this window cannot contribute evidence to that batch.
const QualityBatchStartWindow = 2 * time.Minute
const QualityBatchLifetime = 12 * time.Minute

type QualityBatchQuestion struct {
	Prompt   string                 `json:"prompt"`
	Expected string                 `json:"expected"`
	Question *QualityRandomQuestion `json:"question"`
}

type QualitySharedBatch struct {
	ID            string                 `json:"id"`
	StartedAt     time.Time              `json:"started_at"`
	StartDeadline time.Time              `json:"start_deadline"`
	ClosesAt      time.Time              `json:"closes_at"`
	Questions     []QualityBatchQuestion `json:"questions"`
}

// Result-only evidence. The raw answer verdict remains independent of whether
// a failed answer has a comparable passing account to justify an account action.
type QualityBatchEvidence struct {
	Route            string    `json:"route"`
	MappedModel      string    `json:"mapped_model"`
	ID               string    `json:"id"`
	StartedAt        time.Time `json:"started_at"`
	StartDeadline    time.Time `json:"start_deadline"`
	ClosesAt         time.Time `json:"closes_at"`
	ComparisonKey    string    `json:"comparison_key"`
	Decision         string    `json:"decision"`
	PeerAccounts     int       `json:"peer_accounts"`
	PeerPassed       int       `json:"peer_passed"`
	PeerFailed       int       `json:"peer_failed"`
	PeerInconclusive int       `json:"peer_inconclusive"`
}

// Ported from ranxi2001/sub2api@f895ea743 without 5xx episodes and OAuth 401 evidence.
type QualityBatchRun struct {
	Apply   bool                   `json:"apply"`
	Plan    *ScheduledTestPlan     `json:"plan"`
	Until   time.Time              `json:"until"`
	Results []*ScheduledTestResult `json:"results,omitempty"`
}

// The persistent repository stages answers and finalizes actions/history in
// one transaction. A crash never needs to replay a billable model request.
type QualitySharedBatchRepository interface {
	StartQualityBatch(context.Context, *QualitySharedBatch, *ScheduledTestPlan, time.Time) (*QualitySharedBatch, error)
	CollectQualityBatch(context.Context, *QualityBatchRun) error
	FinalizeQualityBatches(context.Context) error
}

type qualitySharedBatchContextKey struct{}

func logQualityBatchFailure() {
	logger.LegacyPrintf("service.scheduled_test_runner", "quality shared batch storage unavailable; no account action applied")
}

func newQualitySharedBatch(now time.Time) (*QualitySharedBatch, error) {
	start := now.UTC().Truncate(time.Minute)
	cfg := &PelicanTestConfig{QuestionKind: QualityRandomQuestionKind, ParallelCount: 8, Quality: &QualityPolicy{}}
	samples, err := newQualityRandomSamples(cfg)
	if err != nil {
		return nil, err
	}
	batch := &QualitySharedBatch{ID: "random-v" + qualityRandomGeneratorVersion + "-" + start.Format("20060102T1504Z"),
		StartedAt: start, StartDeadline: start.Add(QualityBatchStartWindow), ClosesAt: start.Add(QualityBatchLifetime)}
	for _, sample := range samples {
		batch.Questions = append(batch.Questions, QualityBatchQuestion{Prompt: sample.Prompt, Expected: sample.Quality.ExpectedAnswer, Question: sample.RandomQuestion})
	}
	return batch, nil
}

func (s *ScheduledTestRunnerService) sharedQualityBatchRepo() QualitySharedBatchRepository {
	repo, _ := s.planRepo.(QualitySharedBatchRepository)
	return repo
}

func (s *ScheduledTestRunnerService) finalizeQualityBatches(ctx context.Context) {
	if repo := s.sharedQualityBatchRepo(); repo != nil {
		if err := repo.FinalizeQualityBatches(ctx); err != nil {
			// Do not include stored answers, rule text or database errors in logs.
			logQualityBatchFailure()
		}
	}
}

func sharedQualitySamples(cfg *PelicanTestConfig, batch *QualitySharedBatch) ([]*PelicanTestConfig, error) {
	if batch == nil || cfg.Quality == nil || cfg.ParallelCount < 1 || cfg.ParallelCount > len(batch.Questions) {
		return nil, fmt.Errorf("quality shared batch unavailable")
	}
	samples := make([]*PelicanTestConfig, cfg.ParallelCount)
	for i := range samples {
		question := batch.Questions[i]
		if question.Question == nil || question.Prompt == "" || question.Expected == "" {
			return nil, fmt.Errorf("quality shared question unavailable")
		}
		copy, policy := *cfg, *cfg.Quality
		copy.Quality = &policy
		copy.Prompt, policy.ExpectedAnswer, copy.RandomQuestion = question.Prompt, question.Expected, question.Question
		copy.QualityBatch = &QualityBatchEvidence{ID: batch.ID, StartedAt: batch.StartedAt,
			StartDeadline: batch.StartDeadline, ClosesAt: batch.ClosesAt, Decision: "pending"}
		samples[i] = &copy
	}
	return samples, nil
}

func qualityComparisonKey(cfg *PelicanTestConfig) string {
	if cfg == nil || cfg.RandomQuestion == nil || cfg.Quality == nil || cfg.QualityBatch == nil {
		return ""
	}
	// Only the account channel is ported; the source fork also compared BPS.
	channel := "account"
	// Include statement, oracle and rubric as well as model/effort/channel;
	// different grading policies cannot supply a passing control for each other.
	value, _ := json.Marshal([]any{cfg.QualityBatch.ID, cfg.RandomQuestion, cfg.Prompt,
		cfg.Quality.ExpectedAnswer, cfg.ModelID, cfg.QualityBatch.MappedModel, cfg.QualityBatch.Route,
		cfg.ReasoningEffort, channel, cfg.Quality.Judge})
	return fmt.Sprintf("%x", sha256.Sum256(value))
}

func qualityBatchSampleEligible(result *ScheduledTestResult) bool {
	if result == nil || result.PelicanConfig == nil || result.PelicanConfig.QualityBatch == nil {
		return false
	}
	b := result.PelicanConfig.QualityBatch
	return b.Route != "" && b.MappedModel != "" && !result.FinishedAt.Before(result.StartedAt) &&
		!result.StartedAt.Before(b.StartedAt) && result.StartedAt.Before(b.StartDeadline) &&
		!result.FinishedAt.After(b.ClosesAt) && qualityComparisonKey(result.PelicanConfig) != ""
}

// EvaluateQualityBatch preserves original verdicts while reducing only the
// action evidence. Wrong answers without a passing peer become inconclusive
// for isolation/restoration; they stay incorrect in the admin history.
func EvaluateQualityBatch(run *QualityBatchRun, peers []*QualityBatchRun) string {
	run.Plan.QualityBatchEvaluated = true
	run.Plan.QualityBatchModels = map[string]string{}
	effective := make([]*ScheduledTestResult, 0, len(run.Results))
	for _, result := range run.Results {
		copy := *result
		cfg := result.PelicanConfig
		if cfg == nil || cfg.QualityBatch == nil {
			copy.Status, copy.QualityJudgment = "failed", nil
			effective = append(effective, &copy)
			continue
		}
		evidence := *cfg.QualityBatch
		evidence.ComparisonKey = qualityComparisonKey(cfg)
		evidence.PeerAccounts, evidence.PeerPassed, evidence.PeerFailed, evidence.PeerInconclusive = 0, 0, 0, 0
		byAccount := map[int64][]*ScheduledTestResult{}
		for _, peer := range peers {
			if !peer.Apply || peer.Plan.AccountID == run.Plan.AccountID {
				continue
			}
			for _, sample := range peer.Results {
				if qualityBatchSampleEligible(sample) && qualityComparisonKey(sample.PelicanConfig) == evidence.ComparisonKey {
					byAccount[peer.Plan.AccountID] = append(byAccount[peer.Plan.AccountID], sample)
				}
			}
		}
		for _, samples := range byAccount {
			evidence.PeerAccounts++
			switch qualityOutcome(samples) {
			case "passed":
				evidence.PeerPassed++
			case "failed":
				evidence.PeerFailed++
			default:
				evidence.PeerInconclusive++
			}
		}
		switch {
		case !run.Apply:
			evidence.Decision = "inconclusive"
			copy.Status, copy.QualityJudgment = "failed", nil
		case evidence.Route == "" || evidence.MappedModel == "":
			evidence.Decision = "inconclusive"
			copy.Status, copy.QualityJudgment = "failed", nil
		case !qualityBatchSampleEligible(result):
			evidence.Decision = "outside_window"
			copy.Status, copy.QualityJudgment = "failed", nil
		case qualityOutcome([]*ScheduledTestResult{result}) == "passed":
			evidence.Decision = "passed"
		case evidence.PeerPassed > 0 && qualityOutcome([]*ScheduledTestResult{result}) == "failed":
			evidence.Decision = "peer_passed"
		case qualityOutcome([]*ScheduledTestResult{result}) == "failed":
			evidence.Decision = "no_passing_peer"
			copy.Status, copy.QualityJudgment = "failed", nil
		default:
			evidence.Decision = "inconclusive"
		}
		if result.Status == "skipped" {
			copy.Status, copy.QualityJudgment = result.Status, result.QualityJudgment
		}
		cfg.QualityBatch = &evidence
		if evidence.MappedModel != "" && evidence.Route != "" {
			mapped, exists := run.Plan.QualityBatchModels[cfg.ModelID]
			if exists && mapped != evidence.MappedModel {
				run.Plan.QualityBatchModels[cfg.ModelID] = "" // Mapping changed between samples.
			} else {
				run.Plan.QualityBatchModels[cfg.ModelID] = evidence.MappedModel
			}
		}
		effective = append(effective, &copy)
	}
	if len(effective) != max(1, len(run.Plan.PelicanConfig.ModelIDs))*run.Plan.PelicanConfig.ParallelCount {
		run.Plan.QualityModelOutcomes = nil
		return "inconclusive"
	}
	run.Plan.QualityModelOutcomes = qualityPlanModelOutcomes(run.Plan, effective)
	return qualityRoundOutcome(effective)
}
