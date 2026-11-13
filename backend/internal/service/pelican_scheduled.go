package service

// Ported from ranxi2001/sub2api@f895ea743 (backend/internal/service/pelican_scheduled.go).
// Only quality rules are executed here; HTML/SVG Pelican tests, the Codex state
// probe, 5xx-triggered rounds, BPS channels and OAuth 401 cleanup are not ported.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

const (
	pelicanErrEmptyOutput  = "Model returned empty output"
	pelicanErrCaptureLimit = "Response exceeds 4 MiB capture limit"
	pelicanErrHistoryLimit = "Output exceeds 2 MiB history limit"

	// qualityErrPlatformUnsupported marks accounts whose answer path does not
	// carry the quality prompt in this fork. Such samples are skipped, never graded.
	qualityErrPlatformUnsupported = "quality_platform_unsupported"
)

func (s *AccountTestService) RunPelicanBackground(ctx context.Context, accountID int64, model string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
	ctx = context.WithValue(ctx, qualityProbeContextKey{}, true)
	started := time.Now()
	snapshot := *cfg
	snapshot.ModelID = model
	snapshot.ActualPrompt = intelligenceTestPrompt(cfg)
	if s.accountRepo != nil {
		account, err := s.accountRepo.GetByID(ctx, accountID)
		if err == nil && account != nil && !qualityProbeSupported(account) {
			finished := time.Now()
			snapshot.ActualPrompt = ""
			return &ScheduledTestResult{Status: "skipped", ErrorMessage: qualityErrPlatformUnsupported, StartedAt: started, FinishedAt: finished, PelicanConfig: &snapshot}, nil
		}
	}
	ctx = withPelicanTestOptions(ctx, pelicanTestOptions{qualityBatch: cfg.QualityBatch})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &pelicanRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	c, _ := gin.CreateTestContext(w)
	c.Request = (&http.Request{Header: make(http.Header)}).WithContext(ctx)
	err := s.TestPelicanAccountConnection(c, accountID, model, intelligenceTestPrompt(cfg), cfg.ReasoningEffort)
	output, message := parsePelicanOutput(w.Body.String())
	if w.overflow {
		output = ""
		message = pelicanErrCaptureLimit
	}
	if err != nil && message == "" {
		message = err.Error()
	}
	if message == "" && strings.TrimSpace(output) == "" {
		// Completed answers are graded by the configured model in the runner.
		message = pelicanErrEmptyOutput
	}
	// Bounded history storage.
	if len(output) > 2<<20 {
		output = ""
		message = pelicanErrHistoryLimit
	}
	status := "success"
	if message != "" {
		status = "failed"
	}
	finished := time.Now()
	return &ScheduledTestResult{Status: status, ResponseText: output, ErrorMessage: message, LatencyMs: finished.Sub(started).Milliseconds(), StartedAt: started, FinishedAt: finished, PelicanConfig: &snapshot}, nil
}

func (s *ScheduledTestRunnerService) runPelicanPlan(ctx context.Context, plan *ScheduledTestPlan) bool {
	now := time.Now()
	next, err := nextPlanRun(plan, now)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "quality plan=%d invalid config: %v", plan.ID, err)
		return false
	}
	// Persisted lease prevents duplicate execution across ticks and server replicas.
	// It also recovers automatically after a process crash.
	until := now.Add(15 * time.Minute).Truncate(time.Microsecond)
	claimed, err := s.planRepo.ClaimPelican(ctx, plan, now, until, next)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "quality plan=%d claim failed: %v", plan.ID, err)
	}
	if err != nil || !claimed {
		return false
	}
	// Re-read quota after claiming. Do not turn a skipped run into a quality
	// verdict, or disturb ownership used by later recovery rounds.
	available, quotaErr := s.qualityQuotaAvailable(ctx, plan)
	if quotaErr != nil || !available {
		logger.LegacyPrintf("service.scheduled_test_runner", "quality plan=%d account=%d skipped: quota unavailable", plan.ID, plan.AccountID)
		finishCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if finishErr := s.planRepo.FinishPelican(finishCtx, plan.ID, until, time.Now()); finishErr != nil {
			logger.LegacyPrintf("service.scheduled_test_runner", "quality plan=%d finish failed: %v", plan.ID, finishErr)
		}
		return false
	}
	// Result-only metadata must not mutate the stored rule or shared plan config.
	snapshot := *plan.PelicanConfig
	snapshot.QualityRestoreDiagnostic = nil
	plan.QualityRestoreDiagnostic = nil
	snapshot.TriggerSource = plan.TriggerSource
	if snapshot.TriggerSource == "" {
		snapshot.TriggerSource = "scheduled"
	}
	plan.PelicanConfig = &snapshot
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	models := []string{plan.ModelID}
	if len(plan.PelicanConfig.ModelIDs) > 0 {
		models = plan.PelicanConfig.ModelIDs
	}
	results := make([]*ScheduledTestResult, len(models)*plan.PelicanConfig.ParallelCount)
	var randomSamples []*PelicanTestConfig
	var randomQuestionErr error
	var sharedBatch *QualitySharedBatch
	if plan.PelicanConfig.QuestionKind == QualityRandomQuestionKind {
		candidate, _ := ctx.Value(qualitySharedBatchContextKey{}).(*QualitySharedBatch)
		if candidate == nil {
			candidate, randomQuestionErr = newQualitySharedBatch(now)
		}
		repo := s.sharedQualityBatchRepo()
		if repo == nil {
			randomQuestionErr = fmt.Errorf("quality shared batch storage unavailable")
		} else if randomQuestionErr == nil {
			sharedBatch, randomQuestionErr = repo.StartQualityBatch(runCtx, candidate, plan, until)
		}
		if randomQuestionErr == nil {
			randomSamples, randomQuestionErr = sharedQualitySamples(plan.PelicanConfig, sharedBatch)
		}
	}
	// Samples are interleaved by model so each model can start before repeats.
	// Bound upstream concurrency while waiting for every result, even failures.
	jobs := make(chan int, len(results))
	for i := range results {
		jobs <- i
	}
	close(jobs)
	var unsupported sync.Map
	allowed, _, catalogErr := s.scheduledSvc.eligibleQualityModels(runCtx, plan)
	var wg sync.WaitGroup
	workers := len(results)
	if workers > 8 {
		workers = 8
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				samplePlan := *plan
				cfg := *plan.PelicanConfig
				samplePlan.ModelID = models[index%len(models)]
				cfg.ModelID = samplePlan.ModelID
				if randomSamples != nil {
					cfg = *randomSamples[index/len(models)]
					cfg.ModelID = samplePlan.ModelID
					if cfg.QualityBatch != nil {
						evidence := *cfg.QualityBatch
						cfg.QualityBatch = &evidence
					}
				}
				samplePlan.PelicanConfig = &cfg
				if randomQuestionErr != nil {
					results[index] = &ScheduledTestResult{Status: "failed", ErrorMessage: "random_question_generation_failed", StartedAt: time.Now(), FinishedAt: time.Now(), PelicanConfig: &cfg,
						QualityJudgment: &QualityJudgment{Verdict: "unknown", Reason: "random_question_generation_failed"}}
					continue
				}
				_, excluded := unsupported.Load(samplePlan.ModelID)
				if catalogErr != nil || excluded || !containsString(allowed, samplePlan.ModelID) {
					reason := "model_unavailable"
					if excluded {
						reason = "model_unsupported"
					}
					if catalogErr != nil {
						reason = "model_catalog_unavailable"
					}
					results[index] = &ScheduledTestResult{Status: "skipped", ErrorMessage: reason, StartedAt: time.Now(), FinishedAt: time.Now(), PelicanConfig: &cfg}
					continue
				}
				result := s.runPelicanSample(runCtx, &samplePlan)
				if qualityModelUnsupported(result.ErrorMessage) {
					unsupported.Store(samplePlan.ModelID, true)
					persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(runCtx), 3*time.Second)
					persistErr := s.scheduledSvc.rememberUnsupportedQualityModel(persistCtx, plan.AccountID, samplePlan.ModelID)
					persistCancel()
					if persistErr != nil {
						logger.LegacyPrintf("service.scheduled_test_runner", "quality model exclusion could not be saved: account=%d", plan.AccountID)
					}
					result.Status = "skipped"
					result.ErrorMessage = "model_unsupported"
					result.QualityJudgment = nil
				}
				results[index] = result
			}
		}()
	}
	wg.Wait()
	// Persist timeout failures with a fresh context even after the request deadline.
	saveCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	if sharedBatch != nil && randomQuestionErr == nil {
		for _, result := range results {
			result.PlanID = plan.ID
			result.QualityRoundID = until.Format(time.RFC3339Nano)
			result.QualityAction = "comparison_pending"
		}
		run := &QualityBatchRun{Apply: true, Plan: plan, Until: until, Results: results}
		if err := s.sharedQualityBatchRepo().CollectQualityBatch(saveCtx, run); err != nil {
			logQualityBatchFailure()
			return false
		}
		// Release worker/concurrency slots before waiting for other accounts.
		// A later tick finalizes this durable run using the original lease/version.
		return true
	}
	qualityAction := "inconclusive"
	if qualityRoundHasResults(results) {
		var actionErr error
		qualityAction, actionErr = s.planRepo.ApplyQualityOutcome(saveCtx, plan, until, qualityRoundOutcome(results))
		if actionErr != nil {
			qualityAction = "action_error"
			logger.LegacyPrintf("service.scheduled_test_runner", "quality plan=%d action failed: %v", plan.ID, actionErr)
		}
	}
	for _, result := range results {
		result.QualityAction = qualityAction
		result.QualityRoundID = until.Format(time.RFC3339Nano)
		result.PelicanConfig.QualityRestoreDiagnostic = nil
		if qualityAction == "restore_conflict" {
			result.PelicanConfig.QualityRestoreDiagnostic = plan.QualityRestoreDiagnostic
		}
		if err := s.scheduledSvc.SaveResult(saveCtx, plan.ID, plan.MaxResults, result); err != nil {
			logger.LegacyPrintf("service.scheduled_test_runner", "quality plan=%d save failed: %v", plan.ID, err)
		}
	}
	if err := s.planRepo.FinishPelican(saveCtx, plan.ID, until, time.Now()); err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "quality plan=%d finish failed: %v", plan.ID, err)
		return false
	}
	return true
}

// Each sample runs in its own goroutine, outside Gin recovery. Always return a
// result so an upstream adapter or judge panic cannot kill the process or leave
// the persistence loop with a nil entry. Panic values may contain request data.
func (s *ScheduledTestRunnerService) runPelicanSample(ctx context.Context, plan *ScheduledTestPlan) (result *ScheduledTestResult) {
	started := time.Now()
	// Own the result metadata; rule/client evidence is not proof of the input
	// assembled by the answer executor.
	var snapshot PelicanTestConfig
	failure := func(message string) *ScheduledTestResult {
		finished := time.Now()
		return &ScheduledTestResult{Status: "failed", ErrorMessage: message, StartedAt: started, FinishedAt: finished, LatencyMs: finished.Sub(started).Milliseconds(), PelicanConfig: &snapshot}
	}
	defer func() {
		if recover() != nil {
			result = failure("scheduled_test_panic: background sample failed")
			logger.LegacyPrintf("service.scheduled_test_runner", "quality plan=%d account=%d sample panicked", plan.ID, plan.AccountID)
		}
	}()
	snapshot = *plan.PelicanConfig
	snapshot.ActualPrompt = ""
	if plan.PelicanConfig.QuestionKind == QualityRandomQuestionKind && (plan.PelicanConfig.RandomQuestion == nil || plan.PelicanConfig.Prompt == "" || plan.PelicanConfig.Quality == nil || plan.PelicanConfig.Quality.ExpectedAnswer == "") {
		return failure("random_question_not_prepared")
	}
	if batch := plan.PelicanConfig.QualityBatch; batch != nil && !time.Now().Before(batch.StartDeadline) {
		return failure("quality_batch_start_window_closed")
	}
	if ctx.Err() == nil {
		// Retain the assembled invocation input even if the adapter fails or
		// panics before returning evidence; this does not assert delivery.
		snapshot.ActualPrompt = intelligenceTestPrompt(plan.PelicanConfig)
	}
	var err error
	result, err = s.runPelicanAnswer(ctx, plan)
	if err != nil {
		return failure(fmt.Sprint(err))
	}
	if result == nil {
		return failure("scheduled_test_empty_result: background sample returned no result")
	}
	if result.PelicanConfig != nil {
		// The trusted executor's snapshot describes the actual answer call.
		snapshot = *result.PelicanConfig
	}
	result.PelicanConfig = &snapshot
	if plan.PelicanConfig.Quality != nil && result.Status == "success" {
		var judgment *QualityJudgment
		if s.judgeQuality != nil {
			judgment = s.judgeQuality(ctx, plan.AccountID, plan.PelicanConfig, result.ResponseText)
		}
		applyQualityJudgment(result, judgment)
		result.FinishedAt = time.Now()
		result.LatencyMs = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
	}
	return result
}

// The generated SSE is captured in memory, so cap it before buffering, not just at persistence.
type pelicanRecorder struct {
	*httptest.ResponseRecorder
	cancel   context.CancelFunc
	overflow bool
}

func (w *pelicanRecorder) Write(data []byte) (int, error) {
	if w.overflow || w.Body.Len()+len(data) > 4<<20 {
		w.overflow = true
		w.cancel()
		return 0, io.ErrShortWrite
	}
	return w.ResponseRecorder.Write(data)
}
func (w *pelicanRecorder) WriteString(data string) (int, error) { return w.Write([]byte(data)) }

func parsePelicanOutput(body string) (string, string) {
	output, message := parseTestSSEOutput(body)
	complete := false
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event TestEvent
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) != nil {
			continue
		}
		if event.Type == "test_complete" {
			complete = event.Success
			if !event.Success && message == "" {
				message = "Generation did not complete successfully"
			}
		}
	}
	if !complete && message == "" {
		message = "Generation stream ended before completion"
	}
	return output, message
}

// intelligenceTestPrompt is the exact text sent to the tested model.
func intelligenceTestPrompt(cfg *PelicanTestConfig) string {
	return cfg.Prompt + "\n\n只输出最终答案，不要解释。"
}
