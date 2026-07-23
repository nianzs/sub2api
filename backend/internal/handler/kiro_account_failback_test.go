package handler

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kirocooldown"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type kiroFailbackSchedulerCache struct {
	accounts []*service.Account
}

func (f *kiroFailbackSchedulerCache) GetSnapshot(context.Context, service.SchedulerBucket) ([]*service.Account, bool, error) {
	return f.accounts, true, nil
}
func (*kiroFailbackSchedulerCache) CaptureBucketWriteToken(_ context.Context, bucket service.SchedulerBucket) (service.SchedulerBucketWriteToken, error) {
	return service.SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}
func (*kiroFailbackSchedulerCache) SetSnapshot(context.Context, service.SchedulerBucket, service.SchedulerBucketWriteToken, []service.Account) error {
	return nil
}
func (*kiroFailbackSchedulerCache) RetireBucket(context.Context, service.SchedulerBucket) error {
	return nil
}
func (*kiroFailbackSchedulerCache) ReopenBucket(_ context.Context, bucket service.SchedulerBucket) (service.SchedulerBucketWriteToken, error) {
	return service.SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}
func (*kiroFailbackSchedulerCache) TryAcquireGroupLifecycleLease(context.Context, int64, time.Duration) (service.SchedulerGroupLifecycleLease, bool, error) {
	return service.SchedulerGroupLifecycleLease{}, false, nil
}
func (*kiroFailbackSchedulerCache) ReleaseGroupLifecycleLease(context.Context, service.SchedulerGroupLifecycleLease) error {
	return nil
}
func (f *kiroFailbackSchedulerCache) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	for _, account := range f.accounts {
		if account != nil && account.ID == id {
			return account, nil
		}
	}
	return nil, nil
}
func (*kiroFailbackSchedulerCache) SetAccount(context.Context, *service.Account) error { return nil }
func (*kiroFailbackSchedulerCache) DeleteAccount(context.Context, int64) error         { return nil }
func (*kiroFailbackSchedulerCache) UpdateLastUsed(context.Context, map[int64]time.Time) error {
	return nil
}
func (*kiroFailbackSchedulerCache) TryLockBucket(context.Context, service.SchedulerBucket, time.Duration) (bool, error) {
	return true, nil
}
func (*kiroFailbackSchedulerCache) UnlockBucket(context.Context, service.SchedulerBucket) error {
	return nil
}
func (*kiroFailbackSchedulerCache) ListBuckets(context.Context) ([]service.SchedulerBucket, error) {
	return nil, nil
}
func (*kiroFailbackSchedulerCache) GetOutboxWatermark(context.Context) (int64, error) {
	return 0, nil
}
func (*kiroFailbackSchedulerCache) SetOutboxWatermark(context.Context, int64) error { return nil }

type kiroFailbackGroupRepo struct {
	group *service.Group
}

func (*kiroFailbackGroupRepo) Create(context.Context, *service.Group) error { return nil }
func (f *kiroFailbackGroupRepo) GetByID(context.Context, int64) (*service.Group, error) {
	return f.group, nil
}
func (f *kiroFailbackGroupRepo) GetByIDLite(context.Context, int64) (*service.Group, error) {
	return f.group, nil
}
func (*kiroFailbackGroupRepo) Update(context.Context, *service.Group) error { return nil }
func (*kiroFailbackGroupRepo) Delete(context.Context, int64) error          { return nil }
func (*kiroFailbackGroupRepo) DeleteCascade(context.Context, int64) ([]int64, error) {
	return nil, nil
}
func (*kiroFailbackGroupRepo) List(context.Context, pagination.PaginationParams) ([]service.Group, *pagination.PaginationResult, error) {
	return nil, nil, nil
}
func (*kiroFailbackGroupRepo) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string, *bool) ([]service.Group, *pagination.PaginationResult, error) {
	return nil, nil, nil
}
func (*kiroFailbackGroupRepo) ListActive(context.Context) ([]service.Group, error) {
	return nil, nil
}
func (*kiroFailbackGroupRepo) ListActiveByPlatform(context.Context, string) ([]service.Group, error) {
	return nil, nil
}
func (*kiroFailbackGroupRepo) ExistsByName(context.Context, string) (bool, error) {
	return false, nil
}
func (*kiroFailbackGroupRepo) GetAccountCount(context.Context, int64) (int64, int64, error) {
	return 0, 0, nil
}
func (*kiroFailbackGroupRepo) DeleteAccountGroupsByGroupID(context.Context, int64) (int64, error) {
	return 0, nil
}
func (*kiroFailbackGroupRepo) GetAccountIDsByGroupIDs(context.Context, []int64) ([]int64, error) {
	return nil, nil
}
func (*kiroFailbackGroupRepo) BindAccountsToGroup(context.Context, int64, []int64) error {
	return nil
}
func (*kiroFailbackGroupRepo) UpdateSortOrders(context.Context, []service.GroupSortOrderUpdate) error {
	return nil
}

type kiroFailbackConcurrencyCache struct{}

func (*kiroFailbackConcurrencyCache) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	return true, nil
}
func (*kiroFailbackConcurrencyCache) ReleaseAccountSlot(context.Context, int64, string) error {
	return nil
}
func (*kiroFailbackConcurrencyCache) GetAccountConcurrency(context.Context, int64) (int, error) {
	return 0, nil
}
func (*kiroFailbackConcurrencyCache) GetAccountConcurrencyBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	result := make(map[int64]int, len(ids))
	for _, id := range ids {
		result[id] = 0
	}
	return result, nil
}
func (*kiroFailbackConcurrencyCache) IncrementAccountWaitCount(context.Context, int64, int) (bool, error) {
	return true, nil
}
func (*kiroFailbackConcurrencyCache) DecrementAccountWaitCount(context.Context, int64) error {
	return nil
}
func (*kiroFailbackConcurrencyCache) GetAccountWaitingCount(context.Context, int64) (int, error) {
	return 0, nil
}
func (*kiroFailbackConcurrencyCache) AcquireUserSlot(context.Context, int64, int, string) (bool, error) {
	return true, nil
}
func (*kiroFailbackConcurrencyCache) ReleaseUserSlot(context.Context, int64, string) error {
	return nil
}
func (*kiroFailbackConcurrencyCache) GetUserConcurrency(context.Context, int64) (int, error) {
	return 0, nil
}
func (*kiroFailbackConcurrencyCache) IncrementWaitCount(context.Context, int64, int) (bool, error) {
	return true, nil
}
func (*kiroFailbackConcurrencyCache) DecrementWaitCount(context.Context, int64) error {
	return nil
}
func (*kiroFailbackConcurrencyCache) GetAccountsLoadBatch(context.Context, []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	return map[int64]*service.AccountLoadInfo{}, nil
}
func (*kiroFailbackConcurrencyCache) GetUsersLoadBatch(context.Context, []service.UserWithConcurrency) (map[int64]*service.UserLoadInfo, error) {
	return map[int64]*service.UserLoadInfo{}, nil
}
func (*kiroFailbackConcurrencyCache) CleanupExpiredAccountSlots(context.Context, int64) error {
	return nil
}
func (*kiroFailbackConcurrencyCache) CleanupExpiredAccountSlotKeys(context.Context) error {
	return nil
}
func (*kiroFailbackConcurrencyCache) CleanupStaleProcessSlots(context.Context, string) error {
	return nil
}

type kiroFailbackUpstream struct {
	statusByAccount       map[int64]int
	transportErrByAccount map[int64]error
	accountIDs            []int64
}

func (u *kiroFailbackUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, accountConcurrency, nil)
}

func (u *kiroFailbackUpstream) DoWithTLS(_ *http.Request, _ string, accountID int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.accountIDs = append(u.accountIDs, accountID)
	if err := u.transportErrByAccount[accountID]; err != nil {
		return nil, err
	}
	status := u.statusByAccount[accountID]
	if status == 0 {
		status = http.StatusOK
	}
	if status != http.StatusOK {
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(`{"message":"upstream rejected account"}`)),
		}, nil
	}

	body := bytes.NewBuffer(nil)
	body.Write(kiroFailbackEventStreamFrame(tEventTypeAssistantResponse, map[string]any{
		"assistantResponseEvent": map[string]any{"content": "healthy response"},
	}))
	body.Write(kiroFailbackEventStreamFrame(tEventTypeMessageMetadata, map[string]any{
		"messageMetadataEvent": map[string]any{
			"tokenUsage": map[string]any{"uncachedInputTokens": 7, "outputTokens": 3},
		},
	}))
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/vnd.amazon.eventstream"},
			"x-request-id": []string{"kiro-healthy-request"},
		},
		Body: io.NopCloser(body),
	}, nil
}

const (
	tEventTypeAssistantResponse = "assistantResponseEvent"
	tEventTypeMessageMetadata   = "messageMetadataEvent"
)

func kiroFailbackEventStreamFrame(eventType string, payload map[string]any) []byte {
	payloadBytes, _ := json.Marshal(payload)
	headerName := []byte(":event-type")
	headerValue := []byte(eventType)
	headersLen := 1 + len(headerName) + 1 + 2 + len(headerValue)
	totalLen := 12 + headersLen + len(payloadBytes) + 4
	frame := make([]byte, totalLen)
	binary.BigEndian.PutUint32(frame[0:4], uint32(totalLen))
	binary.BigEndian.PutUint32(frame[4:8], uint32(headersLen))
	offset := 12
	frame[offset] = byte(len(headerName))
	offset++
	copy(frame[offset:], headerName)
	offset += len(headerName)
	frame[offset] = 7
	offset++
	binary.BigEndian.PutUint16(frame[offset:offset+2], uint16(len(headerValue)))
	offset += 2
	copy(frame[offset:], headerValue)
	offset += len(headerValue)
	copy(frame[offset:], payloadBytes)
	return frame
}

type kiroFailbackCooldownStore struct{}

func (*kiroFailbackCooldownStore) ReserveRequest(context.Context, string) (time.Duration, error) {
	return 0, nil
}
func (*kiroFailbackCooldownStore) MarkSuccess(context.Context, string) error { return nil }
func (*kiroFailbackCooldownStore) Mark429(context.Context, string) (time.Duration, error) {
	return time.Minute, nil
}
func (*kiroFailbackCooldownStore) MarkSuspended(context.Context, string) (time.Duration, error) {
	return time.Minute, nil
}
func (*kiroFailbackCooldownStore) GetState(context.Context, string) (*kirocooldown.State, error) {
	return nil, nil
}
func (*kiroFailbackCooldownStore) ClearEarliestTransientCooldown(context.Context, []string) (bool, error) {
	return false, nil
}

type kiroFailbackUsageLogRepo struct {
	service.UsageLogRepository
	logs []*service.UsageLog
}

func (r *kiroFailbackUsageLogRepo) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	r.logs = append(r.logs, log)
	return true, nil
}

func newKiroFailbackHandler(t *testing.T, statusByAccount map[int64]int) (*GatewayHandler, *kiroFailbackUpstream, *kiroFailbackUsageLogRepo, *service.APIKey) {
	t.Helper()

	groupID := int64(9200)
	group := &service.Group{
		ID:             groupID,
		Hydrated:       true,
		Platform:       service.PlatformKiro,
		Status:         service.StatusActive,
		RateMultiplier: 1,
	}
	accounts := []*service.Account{
		newKiroFailbackAccount(9201, "bad", 1, groupID),
		newKiroFailbackAccount(9202, "healthy", 2, groupID),
	}
	schedulerSnapshot := service.NewSchedulerSnapshotService(&kiroFailbackSchedulerCache{accounts: accounts}, nil, nil, nil, nil)
	upstream := &kiroFailbackUpstream{statusByAccount: statusByAccount}
	usageRepo := &kiroFailbackUsageLogRepo{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.MaxAccountSwitches = 4
	cfg.Gateway.MaxLineSize = 1 << 20
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)

	gatewayService := service.NewGatewayService(
		nil,
		&kiroFailbackGroupRepo{group: group},
		usageRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		schedulerSnapshot,
		nil,
		service.NewBillingService(cfg, nil),
		nil,
		billingCache,
		nil,
		upstream,
		&service.DeferredService{},
		nil,
		nil,
		&kiroFailbackCooldownStore{},
		nil,
		nil,
		nil,
		nil,
		&service.TLSFingerprintProfileService{},
		nil,
		nil,
		nil,
		nil,
	)
	concurrencyService := service.NewConcurrencyService(&kiroFailbackConcurrencyCache{})
	handler := NewGatewayHandler(
		gatewayService, nil, nil, nil, nil, concurrencyService, billingCache, nil, &service.APIKeyService{},
		nil, nil, nil, nil, nil, cfg, nil,
	)
	apiKey := &service.APIKey{
		ID:      9203,
		UserID:  9204,
		GroupID: &groupID,
		Group:   group,
		Status:  service.StatusActive,
		User:    &service.User{ID: 9204, Status: service.StatusActive, Concurrency: 10, Balance: 100},
	}
	return handler, upstream, usageRepo, apiKey
}

func newKiroFailbackAccount(id int64, name string, priority int, groupID int64) *service.Account {
	return &service.Account{
		ID:          id,
		Name:        name,
		Platform:    service.PlatformKiro,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    priority,
		Credentials: map[string]any{"api_key": "ksk_" + name, "api_region": "us-east-1"},
		AccountGroups: []service.AccountGroup{
			{AccountID: id, GroupID: groupID},
		},
	}
}

func performKiroFailbackRequest(t *testing.T, handler *GatewayHandler, apiKey *service.APIKey, endpoint string) *httptest.ResponseRecorder {
	t.Helper()
	var body string
	var call func(*gin.Context)
	switch endpoint {
	case EndpointChatCompletions:
		body = `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"stream":false}`
		call = handler.ChatCompletions
	case EndpointResponses:
		body = `{"model":"claude-sonnet-4-6","input":"hello","stream":false}`
		call = handler.Responses
	default:
		t.Fatalf("unsupported endpoint %q", endpoint)
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, endpoint, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})
	call(c)
	return recorder
}

func TestGatewayHandlerKiroAccountFailback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{EndpointChatCompletions, EndpointResponses} {
		t.Run(endpoint+"_retries_402_on_next_account", func(t *testing.T) {
			handler, upstream, usageRepo, apiKey := newKiroFailbackHandler(t, map[int64]int{9201: http.StatusPaymentRequired})

			response := performKiroFailbackRequest(t, handler, apiKey, endpoint)

			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Equal(t, []int64{9201, 9202}, upstream.accountIDs)
			require.Len(t, usageRepo.logs, 1, "only the successful attempt may record usage")
			require.Equal(t, int64(9202), usageRepo.logs[0].AccountID)
		})

		t.Run(endpoint+"_exhausts_each_account_once", func(t *testing.T) {
			handler, upstream, usageRepo, apiKey := newKiroFailbackHandler(t, map[int64]int{
				9201: http.StatusPaymentRequired,
				9202: http.StatusPaymentRequired,
			})

			response := performKiroFailbackRequest(t, handler, apiKey, endpoint)

			require.Equal(t, http.StatusPaymentRequired, response.Code, response.Body.String())
			require.Equal(t, []int64{9201, 9202}, upstream.accountIDs)
			require.Empty(t, usageRepo.logs)
		})

		t.Run(endpoint+"_does_not_retry_terminal_400", func(t *testing.T) {
			handler, upstream, usageRepo, apiKey := newKiroFailbackHandler(t, map[int64]int{9201: http.StatusBadRequest})

			response := performKiroFailbackRequest(t, handler, apiKey, endpoint)

			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			require.Equal(t, []int64{9201}, upstream.accountIDs)
			require.Empty(t, usageRepo.logs)
		})

		t.Run(endpoint+"_does_not_retry_plain_transport_error", func(t *testing.T) {
			handler, upstream, usageRepo, apiKey := newKiroFailbackHandler(t, nil)
			upstream.transportErrByAccount = map[int64]error{9201: errors.New("sensitive transport detail")}

			response := performKiroFailbackRequest(t, handler, apiKey, endpoint)

			require.Equal(t, http.StatusBadGateway, response.Code, response.Body.String())
			require.NotEmpty(t, upstream.accountIDs)
			require.NotContains(t, upstream.accountIDs, int64(9202), "plain transport errors must not fail over to another account")
			require.NotContains(t, response.Body.String(), "sensitive transport detail")
			require.Contains(t, response.Body.String(), "Upstream request failed")
			require.Empty(t, usageRepo.logs)
		})
	}
}
