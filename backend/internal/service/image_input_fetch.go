package service

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
)

// 输入图（参考图）的抓取限制。
const (
	inputImageMaxBytes = 20 << 20 // 20 MiB
	inputImageTimeout  = 30 * time.Second
)

var errInputImageBlocked = errors.New("input image url is not allowed")
var errInputImageFetchFailed = errors.New("fetch input image failed")

// inputImage 是一张待上传到上游的参考图（images/edits 的输入图）。
type inputImage struct {
	Data        []byte
	ContentType string
}

// inputImagePort 是输入图 URL 唯一允许的端口。
const inputImagePort = "443"

// newInputImageClient 构造抓取参考图用的 HTTP 客户端。
//
// 安全要点（SSRF 三层防护）：请求前限定 https+域名+443；Dialer.Control 校验
// 实际连接的 IP（挡 DNS rebinding）；完全不跟随重定向。
func newInputImageClient() *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   inputImageDialControl,
	}
	transport := &http.Transport{
		// 显式不走代理：经代理时 Dialer.Control 看到的是代理地址，校验会失效。
		Proxy:               nil,
		DialContext:         dialer.DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   inputImageTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// inputImageDialControl 在建立连接前校验目标 IP 与端口。
func inputImageDialControl(_, address string, _ syscall.RawConn) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return errInputImageBlocked
	}
	if port != inputImagePort {
		return fmt.Errorf("%w: port %s", errInputImageBlocked, port)
	}
	ip := net.ParseIP(host)
	if ip == nil || !isPublicUnicastIP(ip) {
		return fmt.Errorf("%w: %s", errInputImageBlocked, host)
	}
	return nil
}

// isPublicUnicastIP 判定 IP 是否为可对外路由的单播地址。
func isPublicUnicastIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() ||
		ip.IsInterfaceLocalMulticast())
}

// validateInputImageURL 校验参考图 URL：只接受 https、域名主机（禁止
// IP 字面量）、443 端口。
func validateInputImageURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: malformed url", errInputImageBlocked)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return nil, fmt.Errorf("%w: scheme must be https, got %q", errInputImageBlocked, parsed.Scheme)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("%w: userinfo is not allowed", errInputImageBlocked)
	}
	if port := parsed.Port(); port != "" && port != inputImagePort {
		return nil, fmt.Errorf("%w: port %s", errInputImageBlocked, port)
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return nil, fmt.Errorf("%w: missing host", errInputImageBlocked)
	}
	if strings.Contains(host, "%") || net.ParseIP(host) != nil {
		return nil, fmt.Errorf("%w: ip address hosts are not allowed: %s", errInputImageBlocked, host)
	}
	if urlvalidator.IsBlockedHost(host) || !isInputImageDomain(host) {
		return nil, fmt.Errorf("%w: host %q is not a public domain", errInputImageBlocked, host)
	}
	parsed.Scheme = "https"
	return parsed, nil
}

// isInputImageDomain 判定 host 是否为合法的多级 ASCII 域名（挡
// 127.1、0x7f.1、0177.0.0.1 等数字 IP 写法与单 label 内网名）。
func isInputImageDomain(host string) bool {
	if len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
				return false
			}
		}
	}
	tld := labels[len(labels)-1]
	return tld[0] >= 'a' && tld[0] <= 'z'
}

// fetchInputImage 把客户端给的图片引用取成字节。data: URL 本地解码。
func fetchInputImage(ctx context.Context, client *http.Client, rawURL string) (*inputImage, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, errors.New("input image url is empty")
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		return decodeInputImageDataURL(trimmed)
	}
	parsed, err := validateInputImageURL(trimmed)
	if err != nil {
		return nil, err
	}
	return doFetchInputImage(ctx, client, parsed)
}

func doFetchInputImage(ctx context.Context, client *http.Client, target *url.URL) (*inputImage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build input image request: %w", err)
	}
	req.Header.Set("Accept", "image/*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errInputImageFetchFailed, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, fmt.Errorf("%w: redirects are not allowed (status %d)", errInputImageBlocked, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: unexpected status %d", errInputImageFetchFailed, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, inputImageMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read body: %w", errInputImageFetchFailed, err)
	}
	if len(data) > inputImageMaxBytes {
		return nil, fmt.Errorf("input image exceeds %d bytes", inputImageMaxBytes)
	}
	if len(data) == 0 {
		return nil, errors.New("input image is empty")
	}

	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		contentType = detectedImageContentType(data)
		if contentType == "" {
			return nil, errors.New("input image url did not return an image")
		}
	}
	return &inputImage{Data: data, ContentType: contentType}, nil
}

func decodeInputImageDataURL(rawURL string) (*inputImage, error) {
	_, payload, found := strings.Cut(rawURL, ",")
	if !found {
		return nil, errors.New("malformed data url")
	}
	meta := rawURL[len("data:"):strings.Index(rawURL, ",")]
	if !strings.Contains(strings.ToLower(meta), ";base64") {
		return nil, errors.New("data url must be base64 encoded")
	}
	payload = strings.TrimSpace(payload)
	if base64.StdEncoding.DecodedLen(len(payload)) > inputImageMaxBytes+2 {
		return nil, fmt.Errorf("input image exceeds %d bytes", inputImageMaxBytes)
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("decode data url: %w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("input image is empty")
	}
	if len(data) > inputImageMaxBytes {
		return nil, fmt.Errorf("input image exceeds %d bytes", inputImageMaxBytes)
	}

	contentType := strings.TrimSpace(strings.Split(meta, ";")[0])
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		contentType = detectedImageContentType(data)
		if contentType == "" {
			return nil, errors.New("data url does not carry an image")
		}
	}
	return &inputImage{Data: data, ContentType: contentType}, nil
}
