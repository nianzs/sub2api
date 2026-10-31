package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/fish"
)

// newFishTestService 构造指向假上游的服务与账号。
func newFishTestService(t *testing.T, handler http.Handler) (*FishImageService, *Account, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	service := NewFishImageService(nil)
	service.clients.options = fish.Options{APIBase: server.URL}
	account := &Account{
		ID:   1,
		Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			fishCredentialToken:    "tok",
			fishCredentialTeamID:   "team-1",
			fishCredentialDeviceID: "dev-1",
		},
	}
	return service, account, server
}

func fishImageRequest(model, size, quality string, n int) *OpenAIImagesRequest {
	return &OpenAIImagesRequest{Model: model, Prompt: "a cat", Size: size, Quality: quality, N: n}
}

func TestFishGenerateCallHappyPath(t *testing.T) {
	pngBytes := []byte("pngdata")
	var generateBody map[string]any
	service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tools/image-video/price":
			_, _ = w.Write([]byte(`{"credits":650,"perItemCredits":650,"pricingVersion":"pv-1"}`))
		case "/tools/image-video/generate":
			generateBody = decodeFishBody(t, r)
			_, _ = w.Write([]byte(`{"id":"grp-1","status":"generating","results":[{"id":"m1","status":"generating"}]}`))
		case "/task/group/grp-1":
			_, _ = w.Write([]byte(`{"id":"grp-1","status":"done","results":[{"id":"m1","status":"done","mediaItem":{"id":"m1","url":"http://` + r.Host + `/img.png"}}]}`))
		case "/img.png":
			_, _ = w.Write(pngBytes)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	result, err := service.Generate(context.Background(), account, "tok", fishImageRequest("gpt-image-2", "1024x1024", "medium", 1))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.GroupID != "grp-1" {
		t.Fatalf("GroupID = %q", result.GroupID)
	}
	// 请求体：像素尺寸 + quality + 计价回显
	params := generateBody["params"].(map[string]any)
	if params["image_size"] != "1024x1024" || params["quality"] != "medium" {
		t.Fatalf("params = %v", params)
	}
	if generateBody["expectedCredits"] != float64(650) || generateBody["pricingVersion"] != "pv-1" {
		t.Fatalf("pricing echo = %v", generateBody)
	}
	// 响应体：单张 b64
	var bodyMap map[string]any
	if err := json.Unmarshal(result.Body, &bodyMap); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	data := bodyMap["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data len = %d", len(data))
	}
	b64 := data[0].(map[string]any)["b64_json"].(string)
	if decoded, _ := base64.StdEncoding.DecodeString(b64); string(decoded) != string(pngBytes) {
		t.Fatalf("b64 mismatch")
	}
	if bodyMap["output_format"] != "png" {
		t.Fatalf("output_format = %v, want png", bodyMap["output_format"])
	}
	// 计费转发：1K 档；总耗时覆盖整个请求（usage 记录的 duration_ms）
	if result.Forward.ImageCount != 1 || result.Forward.ImageSize != ImageBillingSize1K {
		t.Fatalf("Forward = %+v", result.Forward)
	}
	if result.Forward.Duration <= 0 {
		t.Fatalf("Forward.Duration = %v, want > 0 (usage duration_ms)", result.Forward.Duration)
	}
}

func TestFishGenerateCallEditsFlow(t *testing.T) {
	png := []byte("ref-image")
	var variant string
	service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/task/upload-url":
			_, _ = w.Write([]byte(`{"object_key":"media_gen_ref/x/image.png","upload_url":"http://` + r.Host + `/r2"}`))
		case "/r2":
			w.WriteHeader(200)
		case "/tools/image-video/media/inspect":
			_, _ = w.Write([]byte(`{"objectKey":"media_gen_ref/x/image.png","width":512,"height":512}`))
		case "/tools/image-video/price":
			_, _ = w.Write([]byte(`{"credits":1820,"pricingVersion":"pv"}`))
		case "/tools/image-video/generate":
			body := decodeFishBody(t, r)
			variant = body["variantKey"].(string)
			_, _ = w.Write([]byte(`{"id":"g","status":"generating","results":[]}`))
		case "/task/group/g":
			_, _ = w.Write([]byte(`{"id":"g","status":"done","results":[{"id":"m","status":"done","mediaItem":{"id":"m","url":"http://` + r.Host + `/o.png"}}]}`))
		case "/o.png":
			_, _ = w.Write([]byte("out"))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	req := fishImageRequest("gpt-image-2", "2048x2048", "medium", 1)
	req.Uploads = []OpenAIImagesUpload{{Data: png, ContentType: "image/png"}}
	if _, err := service.Generate(context.Background(), account, "tok", req); err != nil {
		t.Fatalf("Generate(edits): %v", err)
	}
	if variant != fish.VariantEdit {
		t.Fatalf("variant = %q, want edit", variant)
	}
}

func TestFishGenerateCallIndeterminateSubmit(t *testing.T) {
	service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tools/image-video/price":
			_, _ = w.Write([]byte(`{"credits":650,"pricingVersion":"pv"}`))
		case "/tools/image-video/generate":
			// 模拟提交阶段连接被掐断：结果不明
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("server does not support hijack")
			}
			conn, _, _ := hijacker.Hijack()
			_ = conn.Close()
		default:
			w.WriteHeader(404)
		}
	}))
	_, err := service.Generate(context.Background(), account, "tok", fishImageRequest("gpt-image-2", "1024x1024", "", 1))
	var indeterminate *fishSubmitIndeterminateError
	if !errors.As(err, &indeterminate) {
		t.Fatalf("err = %v, want fishSubmitIndeterminateError", err)
	}
}

func TestFishGenerateCallPollTransientRetry(t *testing.T) {
	// 恢复间隔缩放
	oldInitial, oldMax := fishPollInitialInterval, fishPollMaxInterval
	fishPollInitialInterval, fishPollMaxInterval = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { fishPollInitialInterval, fishPollMaxInterval = oldInitial, oldMax })

	var pollCount int32
	service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tools/image-video/price":
			_, _ = w.Write([]byte(`{"credits":650,"pricingVersion":"pv"}`))
		case "/tools/image-video/generate":
			_, _ = w.Write([]byte(`{"id":"g","status":"generating","results":[]}`))
		case "/task/group/g":
			if atomic.AddInt32(&pollCount, 1) <= 2 {
				w.WriteHeader(http.StatusBadGateway) // 暂态
				return
			}
			_, _ = w.Write([]byte(`{"id":"g","status":"done","results":[{"id":"m","status":"done","mediaItem":{"id":"m","url":"http://` + r.Host + `/i.png"}}]}`))
		case "/i.png":
			_, _ = w.Write([]byte("img"))
		default:
			w.WriteHeader(404)
		}
	}))
	result, err := service.Generate(context.Background(), account, "tok", fishImageRequest("gpt-image-2", "", "", 1))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if pollCount < 3 {
		t.Fatalf("pollCount = %d, want >=3 (transient retried)", pollCount)
	}
	if result.Forward.ImageCount != 1 {
		t.Fatalf("ImageCount = %d", result.Forward.ImageCount)
	}
}

func TestFishGenerateCallFailedGroup(t *testing.T) {
	service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tools/image-video/price":
			_, _ = w.Write([]byte(`{"credits":650,"pricingVersion":"pv"}`))
		case "/tools/image-video/generate":
			_, _ = w.Write([]byte(`{"id":"g","status":"generating","results":[]}`))
		case "/task/group/g":
			_, _ = w.Write([]byte(`{"id":"g","status":"failed","results":[{"id":"m","status":"failed","failureReason":"moderation"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	_, err := service.Generate(context.Background(), account, "tok", fishImageRequest("gpt-image-2", "", "", 1))
	var failed *fishGenerationFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("err = %v, want fishGenerationFailedError", err)
	}
	if failed.reason != "moderation" {
		t.Fatalf("reason = %q", failed.reason)
	}
}

func TestFishGenerateCallUnsupportedSizeIsRequestError(t *testing.T) {
	service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream should not be called for invalid size")
		w.WriteHeader(404)
	}))
	_, err := service.Generate(context.Background(), account, "tok", fishImageRequest("gpt-image-2", "123x456", "", 1))
	if err == nil {
		t.Fatal("expected error")
	}
	failure := classifyFishError(err)
	if failure.Failover == nil || failure.Failover.NextAccountAction != NextAccountStop {
		t.Fatalf("classify = %+v", failure.Failover)
	}
}

func TestFishGenerateCallDynamicFitSize(t *testing.T) {
	// 880x1168（≈3:4 微畸变）：限制 {8,2} 时规划为 1536x2048 原生档，
	// 出图后先裁后缩回请求尺寸；响应回显与计费都按交付尺寸（长边 1168 → 2K）。
	nativePNG := newFitTestPNG(t, 1536, 2048)
	var generateBody map[string]any
	service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tools/image-video/price":
			_, _ = w.Write([]byte(`{"credits":650,"perItemCredits":650,"pricingVersion":"pv-1"}`))
		case "/tools/image-video/generate":
			generateBody = decodeFishBody(t, r)
			_, _ = w.Write([]byte(`{"id":"grp-1","status":"generating","results":[{"id":"m1","status":"generating"}]}`))
		case "/task/group/grp-1":
			_, _ = w.Write([]byte(`{"id":"grp-1","status":"done","results":[{"id":"m1","status":"done","mediaItem":{"id":"m1","url":"http://` + r.Host + `/img.png"}}]}`))
		case "/img.png":
			_, _ = w.Write(nativePNG)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	limits := domain.FishFitLimits{MaxCropPixels: 8, MaxUpscaleFactor: 2.0}
	call := NewFishImageCall(fishImageRequest("gpt-image-2", "880x1168", "medium", 1), "gpt-image-2", limits)
	result, err := service.GenerateCall(context.Background(), account, "tok", call)
	if err != nil {
		t.Fatalf("GenerateCall: %v", err)
	}
	// 上游收到就近原生档
	params := generateBody["params"].(map[string]any)
	if params["image_size"] != "1536x2048" {
		t.Fatalf("image_size = %v, want 1536x2048", params["image_size"])
	}
	// 响应回显客户请求尺寸；交付图实际 880x1168
	var bodyMap map[string]any
	if err := json.Unmarshal(result.Body, &bodyMap); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if bodyMap["size"] != "880x1168" {
		t.Fatalf("body size = %v, want 880x1168", bodyMap["size"])
	}
	if result.Size != "880x1168" {
		t.Fatalf("result.Size = %q, want 880x1168", result.Size)
	}
	b64 := bodyMap["data"].([]any)[0].(map[string]any)["b64_json"].(string)
	decoded, _ := base64.StdEncoding.DecodeString(b64)
	img, _, err := image.Decode(bytes.NewReader(decoded))
	if err != nil {
		t.Fatalf("decode delivered image: %v", err)
	}
	if img.Bounds().Dx() != 880 || img.Bounds().Dy() != 1168 {
		t.Fatalf("delivered = %dx%d, want 880x1168", img.Bounds().Dx(), img.Bounds().Dy())
	}
	// 计费按交付尺寸（880x1168 → 2K）
	if result.Forward.ImageSize != ImageBillingSize2K {
		t.Fatalf("billing = %v, want 2K", result.Forward.ImageSize)
	}
}

func TestFishGenerateCallDynamicFitBeyondLimitsIsRequestError(t *testing.T) {
	// 同尺寸但裁剪限制收紧到 4px（实际需 5.33px）：规划失败，不打上游。
	service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream should not be called for unplannable size")
		w.WriteHeader(404)
	}))
	limits := domain.FishFitLimits{MaxCropPixels: 4, MaxUpscaleFactor: 2.0}
	call := NewFishImageCall(fishImageRequest("gpt-image-2", "880x1168", "medium", 1), "gpt-image-2", limits)
	_, err := service.GenerateCall(context.Background(), account, "tok", call)
	if err == nil {
		t.Fatal("expected error")
	}
	failure := classifyFishError(err)
	if failure.Failover == nil || failure.Failover.NextAccountAction != NextAccountStop {
		t.Fatalf("classify = %+v", failure.Failover)
	}
}

func decodeFishBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

// fishTestPNG 是一张可解码的 1x1 PNG，供格式转换断言使用。
func fishTestPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestFishGenerateCallOutputFormat(t *testing.T) {
	pngBytes := fishTestPNG(t)
	serve := func(body []byte) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/tools/image-video/price":
				_, _ = w.Write([]byte(`{"credits":650,"pricingVersion":"pv"}`))
			case "/tools/image-video/generate":
				_, _ = w.Write([]byte(`{"id":"g","status":"generating","results":[]}`))
			case "/task/group/g":
				_, _ = w.Write([]byte(`{"id":"g","status":"done","results":[{"id":"m","status":"done","mediaItem":{"id":"m","url":"http://` + r.Host + `/img.png"}}]}`))
			case "/img.png":
				_, _ = w.Write(body)
			default:
				t.Errorf("unexpected path %q", r.URL.Path)
				w.WriteHeader(404)
			}
		})
	}

	t.Run("png stays official bytes", func(t *testing.T) {
		service, account, _ := newFishTestService(t, serve(pngBytes))
		req := fishImageRequest("gpt-image-2", "1024x1024", "medium", 1)
		req.OutputFormat = "png"
		result, err := service.Generate(context.Background(), account, "tok", req)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		decoded := fishResponseImage(t, result.Body)
		if string(decoded) != string(pngBytes) {
			t.Fatal("png request re-encoded official bytes")
		}
		if fishResponseFormat(t, result.Body) != "png" {
			t.Fatalf("output_format = %s", fishResponseFormat(t, result.Body))
		}
	})

	t.Run("jpeg converts", func(t *testing.T) {
		service, account, _ := newFishTestService(t, serve(pngBytes))
		req := fishImageRequest("gpt-image-2", "1024x1024", "medium", 1)
		req.OutputFormat = "jpg"
		result, err := service.Generate(context.Background(), account, "tok", req)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		decoded := fishResponseImage(t, result.Body)
		if _, format, err := image.DecodeConfig(bytes.NewReader(decoded)); err != nil || format != "jpeg" {
			t.Fatalf("format = %q err=%v", format, err)
		}
		if fishResponseFormat(t, result.Body) != "jpeg" {
			t.Fatalf("output_format = %s", fishResponseFormat(t, result.Body))
		}
		if _, err := jpeg.Decode(bytes.NewReader(decoded)); err != nil {
			t.Fatalf("jpeg decode: %v", err)
		}
	})
}

func TestFishGenerateCallRejectsOutputFormatBeforeSubmit(t *testing.T) {
	for _, format := range []string{"webp", "gif"} {
		t.Run(format, func(t *testing.T) {
			var hits atomic.Int32
			service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(404)
			}))
			req := fishImageRequest("gpt-image-2", "1024x1024", "medium", 1)
			req.OutputFormat = format
			_, err := service.Generate(context.Background(), account, "tok", req)
			if err == nil {
				t.Fatal("expected error")
			}
			if hits.Load() != 0 {
				t.Fatalf("upstream hits = %d, want 0", hits.Load())
			}
			if !strings.Contains(err.Error(), "output_format") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func fishResponseImage(t *testing.T, body []byte) []byte {
	t.Helper()
	var bodyMap map[string]any
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	data := bodyMap["data"].([]any)
	b64 := data[0].(map[string]any)["b64_json"].(string)
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode b64: %v", err)
	}
	return decoded
}

func fishResponseFormat(t *testing.T, body []byte) string {
	t.Helper()
	var bodyMap map[string]any
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	format, _ := bodyMap["output_format"].(string)
	return format
}

func TestFishGenerateCallCapsQualityToAccountMax(t *testing.T) {
	cases := []struct {
		name      string
		max       string
		requested string
		upstream  string
		echoed    string
	}{
		{name: "caps high to medium", max: "medium", requested: "high", upstream: "medium", echoed: "high"},
		{name: "normalizes hd before capping", max: "medium", requested: "hd", upstream: "medium", echoed: "high"},
		{name: "keeps a lower request", max: "high", requested: "low", upstream: "low", echoed: "low"},
		{name: "unlimited keeps the request", max: "", requested: "high", upstream: "high", echoed: "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var priceQuality, generateQuality string
			service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/tools/image-video/price":
					body := decodeFishBody(t, r)
					params, _ := body["params"].(map[string]any)
					priceQuality, _ = params["quality"].(string)
					_, _ = w.Write([]byte(`{"credits":650,"perItemCredits":650,"pricingVersion":"pv-1"}`))
				case "/tools/image-video/generate":
					body := decodeFishBody(t, r)
					params, _ := body["params"].(map[string]any)
					generateQuality, _ = params["quality"].(string)
					_, _ = w.Write([]byte(`{"id":"grp-1","status":"generating","results":[{"id":"m1","status":"generating"}]}`))
				case "/task/group/grp-1":
					_, _ = w.Write([]byte(`{"id":"grp-1","status":"done","results":[{"id":"m1","status":"done","mediaItem":{"id":"m1","url":"http://` + r.Host + `/img.png"}}]}`))
				case "/img.png":
					_, _ = w.Write([]byte("pngdata"))
				default:
					t.Errorf("unexpected path %q", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			account.Platform = PlatformFish
			if tc.max != "" {
				account.Extra = map[string]any{fishMaxQualityExtraKey: tc.max}
			}

			result, err := service.Generate(context.Background(), account, "tok", fishImageRequest("gpt-image-2", "1024x1024", tc.requested, 1))
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if priceQuality != tc.upstream || generateQuality != tc.upstream {
				t.Fatalf("upstream quality price=%q generate=%q, want %q", priceQuality, generateQuality, tc.upstream)
			}
			if result.Quality != tc.echoed {
				t.Fatalf("result quality = %q, want %q", result.Quality, tc.echoed)
			}
			var bodyMap map[string]any
			if err := json.Unmarshal(result.Body, &bodyMap); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if bodyMap["quality"] != tc.echoed {
				t.Fatalf("response quality = %v, want %q", bodyMap["quality"], tc.echoed)
			}
		})
	}
}

func TestFishGenerateCallExtendedQuality(t *testing.T) {
	for _, tc := range []struct {
		model    string
		quality  string
		upstream string
	}{
		{model: "gpt-image-2.5-flare", quality: "max", upstream: "max"},
		{model: "gpt-image-2.5-sunburst", quality: "xhigh", upstream: "xhigh"},
		{model: "gpt-image-2.5-flare", quality: "maximum", upstream: "max"},
	} {
		t.Run(tc.model+"/"+tc.quality, func(t *testing.T) {
			var paramsQuality, frontendQuality string
			service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/tools/image-video/price":
					body := decodeFishBody(t, r)
					params, _ := body["params"].(map[string]any)
					paramsQuality, _ = params["quality"].(string)
					_, _ = w.Write([]byte(`{"credits":650,"perItemCredits":650,"pricingVersion":"pv-1"}`))
				case "/tools/image-video/generate":
					body := decodeFishBody(t, r)
					frontend, _ := body["frontendParams"].(map[string]any)
					frontendQuality, _ = frontend["quality"].(string)
					_, _ = w.Write([]byte(`{"id":"grp-1","status":"generating","results":[{"id":"m1","status":"generating"}]}`))
				case "/task/group/grp-1":
					_, _ = w.Write([]byte(`{"id":"grp-1","status":"done","results":[{"id":"m1","status":"done","mediaItem":{"id":"m1","url":"http://` + r.Host + `/img.png"}}]}`))
				case "/img.png":
					_, _ = w.Write([]byte("pngdata"))
				default:
					t.Errorf("unexpected path %q", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			account.Platform = PlatformFish
			result, err := service.Generate(context.Background(), account, "tok", fishImageRequest(tc.model, "1024x1024", tc.quality, 1))
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if paramsQuality != tc.upstream || frontendQuality != tc.upstream {
				t.Fatalf("upstream params=%q frontend=%q, want %q", paramsQuality, frontendQuality, tc.upstream)
			}
			if result.Quality != tc.upstream {
				t.Fatalf("echo = %q, want %q", result.Quality, tc.upstream)
			}
		})
	}
}

func TestFishGenerateCallRejectsExtendedQualityOnImage2(t *testing.T) {
	service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("gpt-image-2 must not call upstream for quality %s", r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
	}))
	account.Platform = PlatformFish
	_, err := service.Generate(context.Background(), account, "tok", fishImageRequest("gpt-image-2", "1024x1024", "max", 1))
	var requestErr *fish.RequestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("err = %v, want request error", err)
	}
}

func TestFishGenerateCallCapsMaxToAccountLimit(t *testing.T) {
	var upstream string
	service, account, _ := newFishTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tools/image-video/price":
			body := decodeFishBody(t, r)
			params, _ := body["params"].(map[string]any)
			upstream, _ = params["quality"].(string)
			_, _ = w.Write([]byte(`{"credits":650,"perItemCredits":650,"pricingVersion":"pv-1"}`))
		case "/tools/image-video/generate":
			_, _ = w.Write([]byte(`{"id":"grp-1","status":"generating","results":[{"id":"m1","status":"generating"}]}`))
		case "/task/group/grp-1":
			_, _ = w.Write([]byte(`{"id":"grp-1","status":"done","results":[{"id":"m1","status":"done","mediaItem":{"id":"m1","url":"http://` + r.Host + `/img.png"}}]}`))
		case "/img.png":
			_, _ = w.Write([]byte("pngdata"))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	account.Platform = PlatformFish
	account.Extra = map[string]any{fishMaxQualityExtraKey: "xhigh"}
	result, err := service.Generate(context.Background(), account, "tok", fishImageRequest("gpt-image-2.5-flare", "1024x1024", "max", 1))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if upstream != "xhigh" {
		t.Fatalf("upstream = %q, want xhigh", upstream)
	}
	if result.Quality != "max" {
		t.Fatalf("echo = %q, want max", result.Quality)
	}
}

func TestFishQualityForRequest(t *testing.T) {
	cases := []struct {
		requested string
		extended  bool
		want      string
		wantErr   bool
	}{
		{requested: "medium", want: "medium"},
		{requested: "standard", want: "medium"},
		{requested: "hd", want: "high"},
		{requested: "high", want: "high"},
		{requested: "low", want: "low"},
		{requested: "", want: "medium"},
		{requested: "auto", want: "medium"},
		{requested: "max", extended: true, want: "max"},
		{requested: "xhigh", extended: true, want: "xhigh"},
		{requested: "extra-high", extended: true, want: "xhigh"},
		{requested: "max", wantErr: true},
		{requested: "xhigh", wantErr: true},
	}
	for _, tc := range cases {
		got, err := fishQualityForRequest(tc.requested, tc.extended)
		if tc.wantErr {
			if err == nil {
				t.Errorf("fishQualityForRequest(%q, %v) err = nil, want error", tc.requested, tc.extended)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("fishQualityForRequest(%q, %v) = %q, %v; want %q", tc.requested, tc.extended, got, err, tc.want)
		}
	}
}

// fishSingleImageUpstream 是交付一张指定图片的假上游；generateBody 非空时记录
// generate 请求体。
func fishSingleImageUpstream(t *testing.T, img []byte, generateBody *map[string]any) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tools/image-video/price":
			_, _ = w.Write([]byte(`{"credits":650,"perItemCredits":650,"pricingVersion":"pv-1"}`))
		case "/tools/image-video/generate":
			if generateBody != nil {
				*generateBody = decodeFishBody(t, r)
			}
			_, _ = w.Write([]byte(`{"id":"grp-1","status":"generating","results":[{"id":"m1","status":"generating"}]}`))
		case "/task/group/grp-1":
			_, _ = w.Write([]byte(`{"id":"grp-1","status":"done","results":[{"id":"m1","status":"done","mediaItem":{"id":"m1","url":"http://` + r.Host + `/img.png"}}]}`))
		case "/img.png":
			_, _ = w.Write(img)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func TestFishGenerateCallBillsDeliveredSizeOnUpscale(t *testing.T) {
	// 1568x672（21:9）只能由 1K 档 1344x576 放大 1.17 倍得到：交付长边 1568
	// 属 2K 档，计费跟交付走，不按 1K 原生档。
	var generateBody map[string]any
	service, account, _ := newFishTestService(t, fishSingleImageUpstream(t, newFitTestPNG(t, 1344, 576), &generateBody))
	call := NewFishImageCall(fishImageRequest("gpt-image-2", "1568x672", "medium", 1), "gpt-image-2", domain.DefaultFishFitLimits())
	result, err := service.GenerateCall(context.Background(), account, "tok", call)
	if err != nil {
		t.Fatalf("GenerateCall: %v", err)
	}
	if params := generateBody["params"].(map[string]any); params["image_size"] != "1344x576" {
		t.Fatalf("image_size = %v, want 1344x576", params["image_size"])
	}
	if result.Size != "1568x672" {
		t.Fatalf("result.Size = %q, want 1568x672", result.Size)
	}
	if result.Forward.ImageSize != ImageBillingSize2K {
		t.Fatalf("billing = %v, want 2K (delivered size)", result.Forward.ImageSize)
	}
}

func TestFishGenerateCallFitFailureEchoesDeliveredSize(t *testing.T) {
	// 上游给了与规划比例不符的 1024x1024：裁剪缩放拒绝多裁、回退原图，
	// 回显与计费都按实际交付的 1024x1024，而不是请求的 1568x672。
	service, account, _ := newFishTestService(t, fishSingleImageUpstream(t, newFitTestPNG(t, 1024, 1024), nil))
	call := NewFishImageCall(fishImageRequest("gpt-image-2", "1568x672", "medium", 1), "gpt-image-2", domain.DefaultFishFitLimits())
	result, err := service.GenerateCall(context.Background(), account, "tok", call)
	if err != nil {
		t.Fatalf("GenerateCall: %v", err)
	}
	var bodyMap map[string]any
	if err := json.Unmarshal(result.Body, &bodyMap); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if result.Size != "1024x1024" || bodyMap["size"] != "1024x1024" {
		t.Fatalf("size echo = %q / %v, want 1024x1024", result.Size, bodyMap["size"])
	}
	if result.Forward.ImageSize != ImageBillingSize1K {
		t.Fatalf("billing = %v, want 1K", result.Forward.ImageSize)
	}
}

func TestFishGenerateCallImage2OmitsFrontendQuality(t *testing.T) {
	// gpt-image-2 没有 frontendParams.quality 的抓包样本：只在 params 里发。
	var generateBody map[string]any
	service, account, _ := newFishTestService(t, fishSingleImageUpstream(t, []byte("pngdata"), &generateBody))
	if _, err := service.Generate(context.Background(), account, "tok", fishImageRequest("gpt-image-2", "1024x1024", "high", 1)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if params := generateBody["params"].(map[string]any); params["quality"] != "high" {
		t.Fatalf("params.quality = %v, want high", params["quality"])
	}
	frontend := generateBody["frontendParams"].(map[string]any)
	if _, ok := frontend["quality"]; ok {
		t.Fatalf("frontendParams.quality must be absent for gpt-image-2, got %v", frontend["quality"])
	}
}

func TestFishBuildPartialResultUsesMappedModelQuality(t *testing.T) {
	// 渠道把对外名映射到 2.5：partial 回显与 GenerateCall 一致按映射后的模型判定 max。
	service := NewFishImageService(nil)
	call := NewFishImageCall(fishImageRequest("my-image", "1024x1024", "max", 4), "gpt-image-2.5-flare", domain.DefaultFishFitLimits())
	call.delivered = [][]byte{[]byte("a")}
	result := service.BuildFishPartialResult(context.Background(), call)
	if result == nil {
		t.Fatal("partial result nil")
	}
	if result.Quality != "max" {
		t.Fatalf("partial quality = %q, want max", result.Quality)
	}
}

func TestFishGenerateCallEstimatesUsage(t *testing.T) {
	// fish 不报 token：按 gpt-image-1 官方表 × 交付像素估算，写进响应体与用量记录。
	service, account, _ := newFishTestService(t, fishSingleImageUpstream(t, []byte("pngdata"), nil))
	result, err := service.Generate(context.Background(), account, "tok", fishImageRequest("gpt-image-2", "1024x1024", "medium", 1))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(result.Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	usage, ok := body["usage"].(map[string]any)
	if !ok {
		t.Fatalf("response body has no usage: %s", result.Body)
	}
	if usage["output_tokens"] != float64(1056) {
		t.Fatalf("usage.output_tokens = %v, want 1056", usage["output_tokens"])
	}
	inputTokens, _ := usage["input_tokens"].(float64)
	if inputTokens <= 0 {
		t.Fatalf("usage.input_tokens = %v, want prompt tokens > 0", usage["input_tokens"])
	}
	forward := result.Forward.Usage
	if forward.OutputTokens != 1056 || forward.ImageOutputTokens != 1056 || forward.InputTokens != int(inputTokens) {
		t.Fatalf("Forward.Usage = %+v, want output/image 1056 and input %v", forward, inputTokens)
	}
	if string(result.Usage) == "" {
		t.Fatal("result.Usage must carry the estimate for the stream path")
	}
}
