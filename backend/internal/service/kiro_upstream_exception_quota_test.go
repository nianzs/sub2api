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

// 带明确额度码的流内信号停到月度重置后换号，不能走通用 402 处理把账号永久停用。
func TestHandleKiroUpstreamExceptionQuotaParksUntilMonthlyReset(t *testing.T) {
	cases := []struct {
		name string
		exc  kiropkg.KiroUpstreamException
	}{
		{"monthly request count", kiropkg.KiroUpstreamException{Type: "ServiceQuotaExceededException", Payload: []byte(`{"message":"You have reached the limit.","reason":"MONTHLY_REQUEST_COUNT"}`)}},
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

// 只有额度措辞、没有明确额度码的流内信号可能只针对本次请求；换号会把同一请求发给下一个账号，
// 停到月底会让一个请求停掉一串账号。只按 429 冷却几分钟后换号，不碰月度停调和账号状态。
func TestHandleKiroUpstreamExceptionQuotaWordingOnlyCoolsDown(t *testing.T) {
	cases := []struct {
		name string
		exc  kiropkg.KiroUpstreamException
	}{
		{"service quota exceeded", kiropkg.KiroUpstreamException{Type: "ServiceQuotaExceededException", Message: "Quota exceeded for this request"}},
		{"usage limit reached", kiropkg.KiroUpstreamException{Type: "ValidationException", Message: "You have reached your usage limit for this month."}},
		{"credits exhausted", kiropkg.KiroUpstreamException{Type: "InternalServerException", Message: "Your credits have been exhausted."}},
		{"overage wording without code", kiropkg.KiroUpstreamException{Type: "AccessDeniedException", Message: "Overage limit reached."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exc := tc.exc
			require.Equal(t, http.StatusTooManyRequests, kiroUpstreamExceptionStatus(&exc))
			c, rec := newKiroExceptionTestContext()
			svc, repo := newKiroQuotaTestService()
			cooldown := &stubKiroCooldownStore{}
			svc.kiroCooldownStore = cooldown

			err := svc.handleKiroUpstreamException(context.Background(), c, newKiroQuotaTestAccount(), "claude-opus-4.7", []byte(`{}`), &exc, http.Header{}, writeKiroMessagesError)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
			require.Len(t, cooldown.mark429Keys, 1, "should cool the account down briefly")
			require.Empty(t, repo.setErrorCalls)
			monthlyReset := nextKiroMonthlyResetUTC(time.Now())
			for _, until := range repo.rateLimitedUntil {
				require.True(t, until.Before(monthlyReset.Add(-time.Hour)), "must not park until the monthly reset")
			}
			require.Zero(t, rec.Body.Len())
		})
	}
}
