package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
)

// Per-account IQ check settings live in account Extra and are edited from the
// account form. The state key is written only by the check (and cleared by a
// manual scheduling toggle); account edits never overwrite it.
const (
	IQCheckEnabledExtraKey = "iq_check_enabled"
	// Without this key the check only records results and never stops scheduling.
	IQCheckAutoDisableExtraKey = "iq_check_auto_disable"
	IQCheckModelExtraKey       = "iq_check_model"
	IQCheckIntervalExtraKey    = "iq_check_interval_minutes"
	IQCheckStateExtraKey       = "iq_check_state"

	IQCheckResultPassed  = "passed"
	IQCheckResultFailed  = "failed"
	IQCheckResultUnknown = "unknown"

	iqCheckDefaultIntervalMinutes = 60
	iqCheckMinIntervalMinutes     = 10
	iqCheckMaxIntervalMinutes     = 24 * 60
	// Wrong answers across the last two rounds (four questions) that stop
	// scheduling. A healthy model still misses a hard question now and then.
	iqCheckWrongToDisable  = 3
	iqCheckMaxConcurrent   = 4
	iqCheckQuestionTimeout = 4 * time.Minute
	iqCheckCycleInterval   = time.Minute
	// Shorter than the cycle so the holder can claim again on its next tick,
	// long enough that no other replica claims in between.
	iqCheckCycleLockTTL = 50 * time.Second
	iqCheckCycleLockKey = "iq_check:cycle"
	// Drawings of soft-deleted accounts are pruned this often per instance.
	iqCheckDrawingPruneInterval = time.Hour
	iqCheckErrorMaxRunes        = 300
)

// IQCheckState is the last check result stored under IQCheckStateExtraKey.
type IQCheckState struct {
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
	NextCheckAt *time.Time `json:"next_check_at,omitempty"`
	Result      string     `json:"result,omitempty"`
	Correct     int        `json:"correct"`
	Total       int        `json:"total"`
	Error       string     `json:"error,omitempty"`
	// Wrong answers in the last round and in the last two rounds together.
	LastRoundWrong int `json:"last_round_wrong"`
	RecentWrong    int `json:"recent_wrong"`
	// Cumulative answered and correct questions for StatsModel: the model's
	// real accuracy, to judge the false-stop rate before stopping automatically.
	Answered        int    `json:"answered"`
	AnsweredCorrect int    `json:"answered_correct"`
	StatsModel      string `json:"stats_model,omitempty"`
	// DisabledByCheck marks that this check turned scheduling off, so only
	// then may a passing round turn it back on.
	DisabledByCheck bool   `json:"disabled_by_check"`
	Model           string `json:"model,omitempty"`
}

type iqCheckAccounts interface {
	FindByExtraField(ctx context.Context, key string, value any) ([]Account, error)
	GetByID(ctx context.Context, id int64) (*Account, error)
	UpdateExtra(ctx context.Context, id int64, updates map[string]any) error
	SetSchedulable(ctx context.Context, id int64, schedulable bool) error
}

// IQCheckService periodically asks enabled OpenAI accounts two generated
// reasoning questions and stops scheduling accounts that keep answering wrong.
type IQCheckService struct {
	accounts     iqCheckAccounts
	drawings     IQCheckDrawingRepository
	probe        func(ctx context.Context, accountID int64, model, prompt string) (string, error)
	newQuestions func() ([]iqCheckQuestion, error)
	lockCache    LeaderLockCache
	instanceID   string
	now          func() time.Time
	slots        chan struct{}
	lastPrune    time.Time

	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex
	started bool
	stopped bool
}

func NewIQCheckService(accounts AccountRepository, tests *AccountTestService, lockCache LeaderLockCache, drawings IQCheckDrawingRepository) *IQCheckService {
	ctx, cancel := context.WithCancel(context.Background())
	s := &IQCheckService{
		accounts:     accounts,
		drawings:     drawings,
		newQuestions: newIQCheckQuestions,
		lockCache:    lockCache,
		instanceID:   uuid.NewString(),
		now:          time.Now,
		slots:        make(chan struct{}, iqCheckMaxConcurrent),
		ctx:          ctx,
		cancel:       cancel,
	}
	if tests != nil {
		s.probe = tests.RunIQCheckProbe
	}
	return s
}

// ProvideIQCheckService starts the process-wide periodic runner.
func ProvideIQCheckService(accounts AccountRepository, tests *AccountTestService, lockCache LeaderLockCache, drawings IQCheckDrawingRepository) *IQCheckService {
	s := NewIQCheckService(accounts, tests, lockCache, drawings)
	s.Start()
	return s
}

func (s *IQCheckService) Start() {
	if s == nil || s.accounts == nil || s.probe == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
		return
	}
	s.started = true
	s.wg.Add(1)
	go s.runLoop()
}

func (s *IQCheckService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *IQCheckService) runLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(iqCheckCycleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.RunDue(s.ctx)
		}
	}
}

// RunDue claims due accounts and checks them in the background. A claim moves
// next_check_at forward before the check starts, so later ticks and other
// replicas skip the account even while its check is still running.
func (s *IQCheckService) RunDue(ctx context.Context) {
	now := s.now()
	if !s.acquireCycleLock(ctx) {
		return
	}
	s.pruneDrawings(ctx, now)
	accounts, err := s.accounts.FindByExtraField(ctx, IQCheckEnabledExtraKey, true)
	if err != nil {
		logger.LegacyPrintf("service.iq_check", "list accounts failed: %v", err)
		return
	}
	for i := range accounts {
		account := &accounts[i]
		state := decodeIQCheckState(account.Extra)
		if !iqCheckDue(account, state, now) {
			continue
		}
		select {
		case s.slots <- struct{}{}:
		default:
			return // Busy: the rest stay due for the next tick.
		}
		next := now.Add(time.Duration(iqCheckIntervalMinutes(account.Extra)) * time.Minute)
		state.NextCheckAt = &next
		if err := s.writeState(ctx, account.ID, state); err != nil {
			<-s.slots
			logger.LegacyPrintf("service.iq_check", "claim failed: account=%d err=%v", account.ID, err)
			continue
		}
		accountID, model := account.ID, iqCheckModel(account.Extra)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-s.slots }()
			s.checkAccount(s.ctx, accountID, model)
		}()
	}
}

// One instance claims accounts per cycle. The lock is left to expire, so a
// replica ticking soon after (even across a minute boundary) does not claim
// the same due accounts again.
func (s *IQCheckService) acquireCycleLock(ctx context.Context) bool {
	if s.lockCache == nil {
		return true
	}
	lockCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	acquired, err := s.lockCache.TryAcquireLeaderLock(lockCtx, iqCheckCycleLockKey, s.instanceID, iqCheckCycleLockTTL)
	if err != nil {
		logger.LegacyPrintf("service.iq_check", "leader lock failed: %v", err)
		return false
	}
	return acquired
}

// pruneDrawings drops drawings of deleted accounts at most once per interval,
// including one saved by a check that was still running when its account was deleted.
func (s *IQCheckService) pruneDrawings(ctx context.Context, now time.Time) {
	if s.drawings == nil || (!s.lastPrune.IsZero() && now.Sub(s.lastPrune) < iqCheckDrawingPruneInterval) {
		return
	}
	s.lastPrune = now
	if removed, err := s.drawings.DeleteForDeletedAccounts(ctx); err != nil {
		logger.LegacyPrintf("service.iq_check", "prune drawings failed: %v", err)
	} else if removed > 0 {
		logger.LegacyPrintf("service.iq_check", "pruned %d drawings of deleted accounts", removed)
	}
}

type iqCheckRound struct {
	result   string
	total    int
	answered int // Questions with an answer; request errors are not answers.
	correct  int
	wrong    int
	err      string
}

func (s *IQCheckService) checkAccount(ctx context.Context, accountID int64, model string) {
	defer func() {
		if recover() != nil {
			logger.LegacyPrintf("service.iq_check", "check panicked: account=%d", accountID)
		}
	}()
	// The drawing runs beside the questions and never affects the verdict.
	var drawing *IQCheckDrawing
	var drawWG sync.WaitGroup
	if s.drawings != nil {
		drawWG.Add(1)
		go func() {
			defer drawWG.Done()
			defer func() {
				if recover() != nil {
					logger.LegacyPrintf("service.iq_check", "drawing panicked: account=%d", accountID)
				}
			}()
			drawing = s.drawPelican(ctx, accountID, model)
		}()
	}
	round := s.runRound(ctx, accountID, model)
	drawWG.Wait()
	if ctx.Err() != nil {
		return // Shutting down: the account is checked again after its interval.
	}
	// Save the drawing first: the list reloads drawings when checked_at changes.
	s.saveDrawing(ctx, drawing)
	if err := s.apply(ctx, accountID, model, round); err != nil {
		logger.LegacyPrintf("service.iq_check", "apply failed: account=%d err=%v", accountID, err)
	}
}

func (s *IQCheckService) runRound(ctx context.Context, accountID int64, model string) iqCheckRound {
	questions, err := s.newQuestions()
	if err != nil {
		return iqCheckRound{result: IQCheckResultUnknown, err: err.Error()}
	}
	correct := make([]bool, len(questions))
	errs := make([]error, len(questions))
	var wg sync.WaitGroup
	for i := range questions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// A panic in the probe path counts as a failed request, not a crashed server.
			defer func() {
				if r := recover(); r != nil {
					errs[i] = fmt.Errorf("probe panicked: %v", r)
					logger.LegacyPrintf("service.iq_check", "question panicked: account=%d err=%v", accountID, r)
				}
			}()
			questionCtx, cancel := context.WithTimeout(ctx, iqCheckQuestionTimeout)
			defer cancel()
			answer, err := s.probe(questionCtx, accountID, model, questions[i].Prompt)
			if err != nil {
				errs[i] = err
				return
			}
			correct[i] = gradeIQCheckAnswer(questions[i], answer)
		}(i)
	}
	wg.Wait()
	round := iqCheckRound{result: IQCheckResultPassed, total: len(questions)}
	for i := range questions {
		switch {
		case errs[i] != nil:
			if round.err == "" {
				round.err = truncateIQCheckError(errs[i].Error())
			}
		case correct[i]:
			round.correct++
		default:
			round.wrong++
		}
	}
	round.answered = round.correct + round.wrong
	// A wrong answer is evidence even if the other request failed.
	if round.wrong > 0 {
		round.result = IQCheckResultFailed
	} else if round.err != "" {
		round.result = IQCheckResultUnknown
	}
	return round
}

// apply records the round and changes scheduling from the account's current state.
func (s *IQCheckService) apply(ctx context.Context, accountID int64, model string, round iqCheckRound) error {
	account, err := s.accounts.GetByID(ctx, accountID)
	if err != nil {
		return err
	}
	if account == nil {
		return nil
	}
	now := s.now()
	state := decodeIQCheckState(account.Extra)
	state.CheckedAt = &now
	state.Result, state.Correct, state.Total, state.Error, state.Model = round.result, round.correct, round.total, round.err, model
	if state.NextCheckAt == nil || state.NextCheckAt.Before(now) {
		next := now.Add(time.Duration(iqCheckIntervalMinutes(account.Extra)) * time.Minute)
		state.NextCheckAt = &next
	}
	if state.StatsModel != model {
		// Accuracy of another model says nothing about this one.
		state.Answered, state.AnsweredCorrect, state.StatsModel = 0, 0, model
	}
	autoDisable, _ := account.Extra[IQCheckAutoDisableExtraKey].(bool)
	before := state
	switch decideIQCheck(&state, round, autoDisable, account.Schedulable, iqCheckAccountExpired(account, now)) {
	case iqCheckDisable:
		if err := s.accounts.SetSchedulable(ctx, accountID, false); err != nil {
			logger.LegacyPrintf("service.iq_check", "disable scheduling failed: account=%d err=%v", accountID, err)
			state.DisabledByCheck = before.DisabledByCheck
		} else {
			logger.LegacyPrintf("service.iq_check", "scheduling disabled after %d wrong answers in two rounds: account=%d", state.RecentWrong, accountID)
		}
	case iqCheckEnable:
		if err := s.accounts.SetSchedulable(ctx, accountID, true); err != nil {
			logger.LegacyPrintf("service.iq_check", "restore scheduling failed: account=%d err=%v", accountID, err)
			state.DisabledByCheck = before.DisabledByCheck
		} else {
			logger.LegacyPrintf("service.iq_check", "scheduling restored: account=%d", accountID)
		}
	}
	return s.writeState(ctx, accountID, state)
}

type iqCheckDecision int

const (
	iqCheckNoChange iqCheckDecision = iota
	iqCheckDisable
	iqCheckEnable
)

// decideIQCheck folds the round into the state and returns the scheduling
// change to make. A round without any answer (all requests failed) changes
// nothing. Restoring does not depend on autoDisable: an account the check
// turned off is turned back on even after switching to record-only.
func decideIQCheck(state *IQCheckState, round iqCheckRound, autoDisable, schedulable, expired bool) iqCheckDecision {
	if state.DisabledByCheck && schedulable {
		// Someone turned scheduling back on: the check no longer owns it.
		state.DisabledByCheck = false
		state.LastRoundWrong, state.RecentWrong = 0, 0
	}
	if round.answered == 0 {
		return iqCheckNoChange
	}
	state.RecentWrong = state.LastRoundWrong + round.wrong
	state.LastRoundWrong = round.wrong
	state.Answered += round.answered
	state.AnsweredCorrect += round.correct
	if state.DisabledByCheck {
		if round.total > 0 && round.correct == round.total && !expired {
			state.DisabledByCheck = false
			state.LastRoundWrong, state.RecentWrong = 0, 0
			return iqCheckEnable
		}
		return iqCheckNoChange
	}
	// An account that is already off was turned off by someone else; leave it to them.
	if autoDisable && schedulable && state.RecentWrong >= iqCheckWrongToDisable {
		state.DisabledByCheck = true
		return iqCheckDisable
	}
	return iqCheckNoChange
}

func (s *IQCheckService) writeState(ctx context.Context, accountID int64, state IQCheckState) error {
	value, err := iqCheckStateValue(state)
	if err != nil {
		return err
	}
	return s.accounts.UpdateExtra(ctx, accountID, map[string]any{IQCheckStateExtraKey: value})
}

// ReleaseIQCheckScheduling hands scheduling back to the operator after a
// manual toggle: the check will not restore an account someone turned off,
// and an account someone turned back on starts a fresh two-round window.
func ReleaseIQCheckScheduling(ctx context.Context, accounts AccountRepository, account *Account) error {
	if account == nil {
		return nil
	}
	state := decodeIQCheckState(account.Extra)
	if !state.DisabledByCheck && state.LastRoundWrong == 0 && state.RecentWrong == 0 {
		return nil
	}
	state.DisabledByCheck = false
	state.LastRoundWrong, state.RecentWrong = 0, 0
	value, err := iqCheckStateValue(state)
	if err != nil {
		return err
	}
	if err := accounts.UpdateExtra(ctx, account.ID, map[string]any{IQCheckStateExtraKey: value}); err != nil {
		return err
	}
	if account.Extra != nil {
		account.Extra[IQCheckStateExtraKey] = value
	}
	return nil
}

func iqCheckStateValue(state IQCheckState) (map[string]any, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func decodeIQCheckState(extra map[string]any) IQCheckState {
	var state IQCheckState
	raw, ok := extra[IQCheckStateExtraKey]
	if !ok || raw == nil {
		return state
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return state
	}
	_ = json.Unmarshal(data, &state)
	return state
}

func iqCheckDue(account *Account, state IQCheckState, now time.Time) bool {
	if account == nil || account.Platform != PlatformOpenAI || !account.IsActive() {
		return false
	}
	if enabled, _ := account.Extra[IQCheckEnabledExtraKey].(bool); !enabled {
		return false
	}
	if !iqCheckAccountAvailable(account, state) {
		return false
	}
	return state.NextCheckAt == nil || !now.Before(*state.NextCheckAt)
}

// iqCheckAccountAvailable skips accounts that could not serve traffic anyway:
// expired, rate-limited, overloaded, cooling down, out of quota, or turned off
// by hand. An account the check itself turned off is still checked, since a
// passing round is the only way the check turns it back on.
func iqCheckAccountAvailable(account *Account, state IQCheckState) bool {
	probe := *account
	if state.DisabledByCheck {
		probe.Schedulable = true
	}
	return probe.IsSchedulable()
}

func iqCheckAccountExpired(account *Account, now time.Time) bool {
	// Mirrors the expiry rule that pauses scheduling (see Account.IsSchedulable).
	return account.AutoPauseOnExpired && account.ExpiresAt != nil && !now.Before(*account.ExpiresAt)
}

func iqCheckModel(extra map[string]any) string {
	if model, ok := extra[IQCheckModelExtraKey].(string); ok && strings.TrimSpace(model) != "" {
		return strings.TrimSpace(model)
	}
	return openai.DefaultTestModel
}

func iqCheckIntervalMinutes(extra map[string]any) int {
	minutes := iqCheckDefaultIntervalMinutes
	switch value := extra[IQCheckIntervalExtraKey].(type) {
	case float64:
		minutes = int(value)
	case int:
		minutes = value
	case int64:
		minutes = int(value)
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			minutes = int(parsed)
		}
	}
	if minutes <= 0 {
		minutes = iqCheckDefaultIntervalMinutes
	}
	return min(max(minutes, iqCheckMinIntervalMinutes), iqCheckMaxIntervalMinutes)
}

func truncateIQCheckError(message string) string {
	message = strings.TrimSpace(message)
	if runes := []rune(message); len(runes) > iqCheckErrorMaxRunes {
		return string(runes[:iqCheckErrorMaxRunes]) + "…"
	}
	return message
}
