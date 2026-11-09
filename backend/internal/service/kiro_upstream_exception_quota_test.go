//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/stretchr/testify/require"
)

// kiroQuotaRecordingRepo 记录停调与 SetError；其它方法未实现，被调用会 panic。
type kiroQuotaRecordingRepo struct {
	internal500AccountRepoStub
	rateLimitedUntil []time.Time
}

func (r *kiroQuotaRecordingRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	r.rateLimitedUntil = append(r.rateLimitedUntil, resetAt)
	return nil
}

func newKiroQuotaTestService() (*GatewayService, *kiroQuotaRecordingRepo) {
	repo := &kiroQuotaRecordingRepo{}
	return &GatewayService{accountRepo: repo, rateLimitService: &RateLimitService{accountRepo: repo}}, repo
}

func newKiroQuotaTestAccount() *Account {
	return &Account{ID: 9, Platform: PlatformKiro, Type: AccountTypeOAuth, Name: "kiro-quota", Status: StatusActive, Schedulable: true}
}

// 流内额度用尽信号一律停到月度重置后换号，不能走通用 402 处理把账号永久停用。
func TestHandleKiroUpstreamExceptionQuotaParksUntilMonthlyReset(t *testing.T) {
	cases := []struct {
		name string
		exc  kiropkg.KiroUpstreamException
	}{
		{"monthly request count", kiropkg.KiroUpstreamException{Type: "ServiceQuotaExceededException", Payload: []byte(`{"message":"You have reached the limit.","reason":"MONTHLY_REQUEST_COUNT"}`)}},
		{"service quota exceeded", kiropkg.KiroUpstreamException{Type: "ServiceQuotaExceededException", Message: "Quota exceeded for this request"}},
		{"usage limit reached", kiropkg.KiroUpstreamException{Type: "ValidationException", Message: "You have reached your usage limit for this month."}},
		{"credits exhausted", kiropkg.KiroUpstreamException{Type: "InternalServerException", Message: "Your credits have been exhausted."}},
		{"overage limit", kiropkg.KiroUpstreamException{Type: "AccessDeniedException", Payload: []byte(`{"message":"You have reached the limit for overages.","reason":"OVERAGE_REQUEST_LIMIT_EXCEEDED"}`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exc := tc.exc
			require.Equal(t, http.StatusPaymentRequired, kiroUpstreamExceptionStatus(&exc))
			c, rec := newKiroExceptionTestContext()
			svc, repo := newKiroQuotaTestService()

			err := svc.handleKiroUpstreamException(context.Background(), c, newKiroQuotaTestAccount(), "claude-opus-4.7", []byte(`{}`), &exc, http.Header{}, writeKiroMessagesError)

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusPaymentRequired, failoverErr.StatusCode)
			require.Empty(t, repo.setErrorCalls, "must not permanently disable the account")
			require.Equal(t, []time.Time{nextKiroMonthlyResetUTC(time.Now())}, repo.rateLimitedUntil)
			require.Zero(t, rec.Body.Len(), "failover must not write a response")
		})
	}
}

// HTTP 402（如 WebSearch 直接返回的）同样只停到月度重置，不能被通用 402 分支永久停用。
func TestHandleKiroHTTPErrorPaymentRequiredDoesNotDisableAccount(t *testing.T) {
	c, rec := newKiroExceptionTestContext()
	svc, repo := newKiroQuotaTestService()
	resp := &http.Response{
		StatusCode: http.StatusPaymentRequired,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"message":"You have reached the limit.","reason":"MONTHLY_REQUEST_COUNT"}`)),
	}

	err := svc.handleKiroHTTPError(context.Background(), resp, c, newKiroQuotaTestAccount(), "claude-opus-4.7", []byte(`{}`))

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusPaymentRequired, failoverErr.StatusCode)
	require.Empty(t, repo.setErrorCalls, "must not permanently disable the account")
	require.Equal(t, []time.Time{nextKiroMonthlyResetUTC(time.Now())}, repo.rateLimitedUntil)
	require.Zero(t, rec.Body.Len())
}
