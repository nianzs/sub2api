package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsErrorListCaptureRepo struct {
	service.OpsRepository
	filter *service.OpsErrorLogFilter
}

func (r *opsErrorListCaptureRepo) ListErrorLogs(_ context.Context, filter *service.OpsErrorLogFilter) (*service.OpsErrorLogList, error) {
	r.filter = filter
	return &service.OpsErrorLogList{Errors: []*service.OpsErrorLog{}, Page: 1, PageSize: 20}, nil
}

func newOpsErrorListTestRouter(repo *opsErrorListCaptureRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	svc := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/errors", NewOpsHandler(svc).GetErrorLogs)
	return router
}

func utcAt(year int, month time.Month, day, hour int) time.Time {
	return time.Date(year, month, day, hour, 0, 0, 0, time.UTC)
}

// 用量明细选「上月」(2026-10-01 ~ 2026-10-31, Asia/Shanghai) 时的真实请求参数。
const opsLastMonthDateQuery = "start_date=2026-10-01&end_date=2026-10-31&timezone=Asia%2FShanghai"

func TestParseOpsErrorLogTimeRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	parse := func(query string, requireBounds bool) (time.Time, time.Time, error) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/?"+query, nil)
		return parseOpsErrorLogTimeRange(c, "1h", requireBounds)
	}

	t.Run("usage-style date range is read in the user timezone with an inclusive end date", func(t *testing.T) {
		start, end, err := parse(opsLastMonthDateQuery, false)
		require.NoError(t, err)
		require.True(t, start.Equal(utcAt(2026, 9, 30, 16)), "start=%s", start)
		require.True(t, end.Equal(utcAt(2026, 10, 31, 16)), "end=%s", end) // 10-31 当天整日 → 次日 00:00 +08:00
	})

	t.Run("date range has no window cap, like the usage list", func(t *testing.T) {
		_, _, err := parse("start_date=2025-01-01&end_date=2026-10-31&timezone=UTC", false)
		require.NoError(t, err)
	})

	t.Run("end date advances by calendar day across a DST change", func(t *testing.T) {
		// America/New_York 2026-03-08 为春季 DST 切换日，当天仅 23 小时：不能按固定 +24h 推进。
		start, end, err := parse("start_date=2026-03-08&end_date=2026-03-08&timezone=America%2FNew_York", false)
		require.NoError(t, err)
		require.True(t, start.Equal(utcAt(2026, 3, 8, 5)), "start=%s", start)
		require.True(t, end.Equal(utcAt(2026, 3, 9, 4)), "end=%s", end)
	})

	t.Run("a missing side stays unbounded", func(t *testing.T) {
		start, end, err := parse("start_date=2026-10-01&timezone=UTC", false)
		require.NoError(t, err)
		require.True(t, start.Equal(utcAt(2026, 10, 1, 0)))
		require.True(t, end.IsZero())

		start, end, err = parse("end_date=2026-10-31&timezone=UTC", false)
		require.NoError(t, err)
		require.True(t, start.IsZero())
		require.True(t, end.Equal(utcAt(2026, 11, 1, 0)))
	})

	t.Run("invalid dates are rejected", func(t *testing.T) {
		_, _, err := parse("start_date=2026-13-01&timezone=UTC", false)
		require.ErrorContains(t, err, "invalid start_date")
		_, _, err = parse("end_date=oops&timezone=UTC", false)
		require.ErrorContains(t, err, "invalid end_date")
	})

	t.Run("date params win over RFC3339 params", func(t *testing.T) {
		start, end, err := parse("start_date=2026-10-01&end_date=2026-10-02&timezone=UTC&start_time=2020-01-01T00:00:00Z&end_time=2020-01-02T00:00:00Z", false)
		require.NoError(t, err)
		require.True(t, start.Equal(utcAt(2026, 10, 1, 0)))
		require.True(t, end.Equal(utcAt(2026, 10, 3, 0)))
	})

	t.Run("legacy RFC3339 path keeps the dashboard 30 day cap", func(t *testing.T) {
		_, _, err := parse("start_time=2026-09-30T16:00:00.000Z&end_time=2026-10-31T15:59:59.999Z", false)
		require.ErrorContains(t, err, "max window is 30 days")
	})

	t.Run("legacy RFC3339 path still works inside the cap", func(t *testing.T) {
		start, end, err := parse("start_time=2026-10-01T00:00:00Z&end_time=2026-10-02T00:00:00Z", false)
		require.NoError(t, err)
		require.True(t, start.Equal(utcAt(2026, 10, 1, 0)))
		require.True(t, end.Equal(utcAt(2026, 10, 2, 0)))
	})

	t.Run("cleanup requires both bounds of the chosen contract", func(t *testing.T) {
		for name, query := range map[string]string{
			"no params":                  "",
			"start_date only":            "start_date=2026-10-01&timezone=UTC",
			"end_date only":              "end_date=2026-10-31&timezone=UTC",
			"start_time only":            "start_time=2026-10-01T00:00:00Z",
			"end_time only":              "end_time=2026-10-02T00:00:00Z",
			"date start + RFC3339 end":   "start_date=2026-10-01&end_time=2026-10-02T00:00:00Z",
			"RFC3339 start + date end":   "start_time=2026-10-01T00:00:00Z&end_date=2026-10-02",
			"date start + RFC3339 range": "start_date=2026-10-01&start_time=2026-10-01T00:00:00Z&end_time=2026-10-02T00:00:00Z",
		} {
			t.Run(name, func(t *testing.T) {
				_, _, err := parse(query, true)
				require.ErrorContains(t, err, "required")
			})
		}

		_, _, err := parse("start_date=2026-10-01&end_date=2026-10-31&timezone=UTC", true)
		require.NoError(t, err)
		_, _, err = parse("start_time=2026-10-01T00:00:00Z&end_time=2026-10-02T00:00:00Z", true)
		require.NoError(t, err)
	})
}

func TestOpsHandlerGetErrorLogsUsesUsageStyleDateRange(t *testing.T) {
	repo := &opsErrorListCaptureRepo{}
	router := newOpsErrorListTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/errors?view=all&page=1&page_size=20&"+opsLastMonthDateQuery, nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NotNil(t, repo.filter)
	require.NotNil(t, repo.filter.StartTime)
	require.NotNil(t, repo.filter.EndTime)
	require.True(t, repo.filter.StartTime.Equal(utcAt(2026, 9, 30, 16)))
	require.True(t, repo.filter.EndTime.Equal(utcAt(2026, 10, 31, 16)))
}

func TestOpsHandlerGetErrorLogsRejectsInvalidDate(t *testing.T) {
	repo := &opsErrorListCaptureRepo{}
	router := newOpsErrorListTestRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/errors?start_date=2026-13-01&timezone=UTC", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "invalid start_date")
	require.Nil(t, repo.filter)
}

func TestOpsHandlerDeleteErrorLogsUsesUsageStyleDateRange(t *testing.T) {
	repo := &opsErrorCleanupCaptureRepo{}
	router := newOpsErrorCleanupTestRouter(repo)

	req := httptest.NewRequest(http.MethodDelete, "/errors?view=all&"+opsLastMonthDateQuery, nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NotNil(t, repo.filter)
	require.NotNil(t, repo.filter.StartTime)
	require.NotNil(t, repo.filter.EndTime)
	require.True(t, repo.filter.StartTime.Equal(utcAt(2026, 9, 30, 16)))
	require.True(t, repo.filter.EndTime.Equal(utcAt(2026, 10, 31, 16)))
}

func TestOpsHandlerDeleteErrorLogsRequiresBothBounds(t *testing.T) {
	for name, query := range map[string]string{
		"no params":       "",
		"start_date only": "start_date=2026-10-01&timezone=UTC",
		"end_date only":   "end_date=2026-10-31&timezone=UTC",
	} {
		t.Run(name, func(t *testing.T) {
			repo := &opsErrorCleanupCaptureRepo{}
			router := newOpsErrorCleanupTestRouter(repo)

			req := httptest.NewRequest(http.MethodDelete, "/errors?"+query, nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Nil(t, repo.filter)
		})
	}
}
