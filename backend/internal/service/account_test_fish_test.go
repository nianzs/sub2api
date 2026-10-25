package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/fish"
	"github.com/gin-gonic/gin"
)

// TestFishAccountTestConnectionBackfillsIdentity 断言连通性测试把
// team_id / device_id 回填进凭据（api_key 原样往返）。
func TestFishAccountTestConnectionBackfillsIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var persisted map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user/self":
			_, _ = w.Write([]byte(`{"_id":"uid-9","email":"a@b.c","nickname":"n","banned":false}`))
		case "/wallet/uid-9/package":
			_, _ = w.Write([]byte(`{"total":8000,"balance":8000,"type":"free"}`))
		case "/tools/image-video/entitlements":
			_, _ = w.Write([]byte(`{"plan":"free","maxImagesPerGeneration":2,"videoAllowed":false}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)

	svc := &AccountTestService{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	account := &Account{
		ID:          42,
		Type:        AccountTypeAPIKey,
		Platform:    PlatformFish,
		Credentials: map[string]any{fishCredentialToken: "tok"},
	}
	// 直接替换 account.Credentials 观察回填：persistAccountCredentials 会写
	// account.Credentials 再尝试 repo（repo 为 nil 时跳过落库）
	err := svc.testFishAccountConnectionWithBase(c, account, server.URL)
	if err != nil {
		t.Fatalf("testFishAccountConnection: %v", err)
	}
	_ = persisted
	// repo 为 nil 时凭据仍更新到内存 account 上
	if account.GetCredential(fishCredentialTeamID) != "uid-9" {
		t.Fatalf("team_id = %q, want uid-9", account.GetCredential(fishCredentialTeamID))
	}
	if account.GetCredential(fishCredentialDeviceID) == "" {
		t.Fatal("device_id not backfilled")
	}
	if account.GetCredential(fishCredentialToken) != "tok" {
		t.Fatalf("api_key must round-trip unchanged")
	}
	if account.GetCredential("plan_type") != "free" {
		t.Fatalf("plan_type = %q, want free", account.GetCredential("plan_type"))
	}
	if strings.Contains(w.Body.String(), "/tools/image-video/price") || strings.Contains(w.Body.String(), "Generating image") {
		t.Fatal("empty model must stay connectivity-only")
	}
	_ = fish.Identity{}
}

func TestFishTokenImageTestSubmitsAndEmitsImage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user/self":
			_, _ = w.Write([]byte(`{"_id":"uid-9","email":"a@b.c","banned":false}`))
		case "/wallet/uid-9/package":
			_, _ = w.Write([]byte(`{"total":8000,"balance":8000,"type":"free"}`))
		case "/tools/image-video/entitlements":
			_, _ = w.Write([]byte(`{"plan":"free","maxImagesPerGeneration":2,"dailyImage":{"limit":3,"used":0,"remaining":3,"resetAt":"2099-03-14T00:00:00Z"}}`))
		case "/tools/image-video/price":
			_, _ = w.Write([]byte(`{"credits":10,"perItemCredits":10,"pricingVersion":"v9"}`))
		case "/tools/image-video/generate":
			_, _ = w.Write([]byte(`{"id":"g1","status":"done","results":[{"id":"r1","status":"done","mediaItem":{"id":"m1","type":"image","url":"https://cdn.example/a.png"}}]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	svc := &AccountTestService{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	account := &Account{
		ID:          42,
		Type:        AccountTypeAPIKey,
		Platform:    PlatformFish,
		Credentials: map[string]any{fishCredentialToken: "tok"},
	}
	if err := svc.testFishTokenConnection(c, account, server.URL, "gpt-image-2", "a red cube"); err != nil {
		t.Fatalf("testFishTokenConnection: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "https://cdn.example/a.png") {
		t.Fatalf("image url missing from SSE: %s", body)
	}
	if !strings.Contains(body, `"success":true`) {
		t.Fatalf("test_complete missing success: %s", body)
	}
}

// fishTestCooldownCase 驱动一次撞 429 的连通性测试并断言落库的冷却。
func fishTestCooldownCase(t *testing.T, account *Account, wantUntil func(time.Time) time.Time, wantReason string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"status":429,"message":"rate limited"}`))
	}))
	t.Cleanup(server.Close)

	repo := &fishUsageRepoStub{}
	svc := &AccountTestService{accountRepo: repo}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	started := time.Now()
	_ = svc.testFishAccountConnectionWithBase(c, account, server.URL)

	if repo.sets != 1 {
		t.Fatalf("cooldown writes = %d, want 1 (repo=%+v body=%s)", repo.sets, repo, w.Body.String())
	}
	if repo.reason != wantReason {
		t.Fatalf("reason = %q, want %q", repo.reason, wantReason)
	}
	// until 落在 [want(started), want(结束时)] 区间内（now 在两取值点之间）。
	if floor, ceil := wantUntil(started), wantUntil(time.Now()); repo.until.Before(floor) || repo.until.After(ceil) {
		t.Fatalf("until = %v, want within [%v, %v]", repo.until, floor, ceil)
	}
	if !strings.Contains(w.Body.String(), "rate limited") {
		t.Fatalf("test SSE missing rate-limited error: %s", w.Body.String())
	}
}

func TestFishAccountTestRateLimitedCoolsFreePlanToMidnight(t *testing.T) {
	account := &Account{
		ID:          42,
		Type:        AccountTypeAPIKey,
		Platform:    PlatformFish,
		Credentials: map[string]any{fishCredentialToken: "tok", fishCredentialPlan: fishPlanFree},
	}
	fishTestCooldownCase(t, account, nextUTCMidnight, fishRateLimitCooldownReason)
}

func TestFishAccountTestRateLimitedCoolsUnknownPlanFiveMinutes(t *testing.T) {
	account := &Account{
		ID:          42,
		Type:        AccountTypeAPIKey,
		Platform:    PlatformFish,
		Credentials: map[string]any{fishCredentialToken: "tok"},
	}
	fishTestCooldownCase(t, account, func(started time.Time) time.Time {
		return started.Add(fishRateLimitCooldown)
	}, fishRateLimitCooldownReason)
}
