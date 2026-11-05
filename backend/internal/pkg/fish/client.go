// Package fish 实现 fish.audio 生图工具（image-video tools）的上游协议
// 客户端。协议依据 2026-10 三轮抓包 + price Oracle 穷举：
// fish-investigation/captures/2026-10-19-imagegen/PROTOCOL.md。
package fish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// APIBase 是 fish 业务 API 基址。
	APIBase = "https://api.fish.audio"
	// WebOrigin 是网页前端来源（CORS/指纹用）。
	WebOrigin = "https://fish.audio"

	// chromeUserAgent 与配套的 sec-ch-ua 头模拟真实 Chrome 用户
	//（组合已在 fish 上游验证可用）。
	chromeUserAgent     = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/154.0.0.0 Safari/537.36"
	chromeSecCHUA       = `"Chromium";v="154", "Google Chrome";v="154", "Not A(Brand";v="99"`
	chromeSecCHUAPlat   = `"macOS"`
	chromeAcceptLang    = "en-US,en;q=0.9"
	maxErrorBodySnippet = 500
	maxDownloadBytes    = 32 << 20

	// sessionIdleRotation 是 x-fish-amp-session-id 的闲置轮换窗口，
	// 对齐 Amplitude SDK 的默认会话超时语义。
	sessionIdleRotation = 30 * time.Minute
)

// Identity 是一个 fish 账号的指纹身份。DeviceID/TeamID 随账号凭据持久化
//（DeviceID 每账号一个 UUID，绝不跨账号复用）；SessionID 由客户端运行时
// 派生并按闲置窗口轮换。TeamID 为空时头不发送（上游已验证可省）。
type Identity struct {
	DeviceID  string
	TeamID    string
	SessionID string
}

// Options 配置 Client。
type Options struct {
	// ProxyURL 可选的出站代理。
	ProxyURL string
	// HTTPClient 可选的自定义 http.Client（测试注入用）。
	HTTPClient *http.Client
	// APIBase 覆盖默认端点（测试注入用）。
	APIBase string
	// Now 覆盖时间源（测试注入用）。
	Now func() time.Time
}

// Client 是 fish 上游的无状态客户端：不持有账号会话，token 与指纹身份
// 每次调用显式传入；仅 SessionID 的轮换状态是客户端内部的。
type Client struct {
	httpClient *http.Client
	apiBase    string
	now        func() time.Time

	sessionMu       sync.Mutex
	sessionID       string
	sessionLastUsed time.Time
}

// NewClient 构造客户端。
func NewClient(opts Options) *Client {
	transport := http.DefaultTransport
	if opts.ProxyURL != "" {
		if parsed, err := url.Parse(opts.ProxyURL); err == nil {
			proxyTransport := http.DefaultTransport.(*http.Transport).Clone()
			proxyTransport.Proxy = http.ProxyURL(parsed)
			transport = proxyTransport
		}
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Transport: transport, Timeout: 120 * time.Second}
	}
	apiBase := strings.TrimRight(strings.TrimSpace(opts.APIBase), "/")
	if apiBase == "" {
		apiBase = APIBase
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Client{httpClient: client, apiBase: apiBase, now: now}
}

// sessionIDFor 返回该身份当前应使用的 amp session id：Identity 显式携带
// 则透传；否则由客户端生成并在闲置超窗口后轮换。
func (c *Client) sessionIDFor(identity Identity) string {
	if id := strings.TrimSpace(identity.SessionID); id != "" {
		return id
	}
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	now := c.now()
	if c.sessionID == "" || now.Sub(c.sessionLastUsed) > sessionIdleRotation {
		c.sessionID = fmt.Sprintf("%d", now.UnixMilli())
	}
	c.sessionLastUsed = now
	return c.sessionID
}

// applyBrowserHeaders 写入完整浏览器指纹头集合。刻意不发送 Referer
//（网页端 referrerPolicy 下本来就没有）、cookie 与 dnt。
func (c *Client) applyBrowserHeaders(req *http.Request, token string, identity Identity) {
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", chromeAcceptLang)
	req.Header.Set("Origin", WebOrigin)
	req.Header.Set("User-Agent", chromeUserAgent)
	req.Header.Set("Sec-Ch-Ua", chromeSecCHUA)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", chromeSecCHUAPlat)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	if deviceID := strings.TrimSpace(identity.DeviceID); deviceID != "" {
		req.Header.Set("X-Fish-Amp-Device-Id", deviceID)
	}
	req.Header.Set("X-Fish-Amp-Session-Id", c.sessionIDFor(identity))
	if teamID := strings.TrimSpace(identity.TeamID); teamID != "" {
		req.Header.Set("X-Team-Id", teamID)
		req.Header.Set("X-Workspace-Id", teamID)
	}
}

// apiErrorEnvelope 是 fish 的统一错误信封。Code 是稳定机器码
//（如 402 的 OUT_OF_CREDITS，2026-10-20 实测），部分错误没有。
type apiErrorEnvelope struct {
	Status  int    `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// doJSON 执行一次 API 调用：body 为 *struct（nil 表示无体），out 非 nil 时
// 解析响应。非 2xx 按 fish 信封分类为类型化错误。
func (c *Client) doJSON(ctx context.Context, method, path string, token string, identity Identity, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return &RequestError{Message: fmt.Sprintf("encode request body: %v", err), ErrorType: ErrorTypeNetwork}
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiBase+path, reader)
	if err != nil {
		return &RequestError{Message: fmt.Sprintf("create request: %v", err), ErrorType: ErrorTypeNetwork}
	}
	req.Close = true
	c.applyBrowserHeaders(req, token, identity)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return wrapNetworkError("fish request failed", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return wrapNetworkError("read fish response", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope apiErrorEnvelope
		_ = json.Unmarshal(raw, &envelope)
		if envelope.Message == "" {
			envelope.Message = string(bytes.TrimSpace(raw))
			if len(envelope.Message) > maxErrorBodySnippet {
				envelope.Message = envelope.Message[:maxErrorBodySnippet]
			}
		}
		code := strings.TrimSpace(envelope.Code)
		if code != "" {
			envelope.Message = code + ": " + envelope.Message
		}
		return withErrorCode(classifyAPIError(resp.StatusCode, envelope.Message), code)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return &RequestError{Message: fmt.Sprintf("decode fish response: %v", err), ErrorType: ErrorTypeStatus, StatusCode: resp.StatusCode}
		}
	}
	return nil
}

// DownloadImage 匿名下载产物（platform.r2.fish.audio 直取，无需鉴权）。
func (c *Client) DownloadImage(ctx context.Context, imageURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, &RequestError{Message: fmt.Sprintf("create download request: %v", err), ErrorType: ErrorTypeNetwork}
	}
	req.Close = true
	req.Header.Set("User-Agent", chromeUserAgent)
	req.Header.Set("Accept", "image/*,application/octet-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, wrapNetworkError("download fish image", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &RequestError{Message: fmt.Sprintf("download fish image: status %d", resp.StatusCode), ErrorType: ErrorTypeStatus, StatusCode: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, wrapNetworkError("read fish image body", err)
	}
	if len(data) > maxDownloadBytes {
		return nil, &RequestError{Message: "fish image exceeds download limit", ErrorType: ErrorTypeStatus}
	}
	return data, nil
}
