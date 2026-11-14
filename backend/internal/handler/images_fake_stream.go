package handler

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const imagesFakeStreamKey = "images_fake_stream"

// imagesFakeStream 为上游不流式的出图平台（fish）的 stream=true 请求提供
// 「假流式」：等待期间按间隔写 SSE 注释保活，出图完成后一次性写出 OpenAI
// Images 流式协议的 completed 事件。
//
// 响应头延迟到第一次保活（或最终写出）时才提交：之前出现的错误（参数校验、
// 无可用账号等）仍能以原状态码返回 JSON；提交之后的错误只能写 SSE error 事件
// （状态码已是 200，与 OpenAI 出图链路的取舍一致）。所有写入都在 mu 下进行，
// 保活 goroutine 与请求 goroutine 不会交错写 c.Writer。
type imagesFakeStream struct {
	c *gin.Context

	mu      sync.Mutex
	started bool // 响应头已提交（SSE）
	paused  bool // 转交中转号期间暂停保活
	stopped bool // 保活已结束（写出终态或请求结束）
	dead    bool // 写客户端失败（已断开）

	done     chan struct{}
	doneOnce sync.Once
}

// startImagesFakeStream 创建假流式会话并挂到 gin context；interval ≤ 0 时不保活
// （仍可在最终写出时提交 SSE 响应）。
func startImagesFakeStream(c *gin.Context, interval time.Duration) *imagesFakeStream {
	s := &imagesFakeStream{c: c, done: make(chan struct{})}
	c.Set(imagesFakeStreamKey, s)
	if interval > 0 {
		go s.keepalive(interval)
	}
	return s
}

func imagesFakeStreamFromContext(c *gin.Context) *imagesFakeStream {
	if c == nil {
		return nil
	}
	value, ok := c.Get(imagesFakeStreamKey)
	if !ok {
		return nil
	}
	s, _ := value.(*imagesFakeStream)
	return s
}

func (s *imagesFakeStream) keepalive(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var reqDone <-chan struct{}
	if s.c.Request != nil {
		reqDone = s.c.Request.Context().Done()
	}
	for {
		select {
		case <-s.done:
			return
		case <-reqDone:
			return
		case <-ticker.C:
			s.beat()
		}
	}
}

func (s *imagesFakeStream) beat() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.paused || s.dead {
		return
	}
	s.commitLocked()
	if _, err := io.WriteString(s.c.Writer, ":\n\n"); err != nil {
		s.dead = true
		return
	}
	s.c.Writer.Flush()
}

// commitLocked 提交 SSE 响应头（只做一次）。
func (s *imagesFakeStream) commitLocked() {
	if s.started {
		return
	}
	header := s.c.Writer.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	s.c.Writer.WriteHeader(http.StatusOK)
	s.started = true
	service.MarkResponseCommitted(s.c)
}

// finishLocked 结束保活（幂等）。
func (s *imagesFakeStream) finishLocked() {
	s.stopped = true
	s.doneOnce.Do(func() { close(s.done) })
}

// committed 报告 SSE 响应头是否已提交。
func (s *imagesFakeStream) committed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

// pause / resume 在转交中转号转发（真流式，自写响应头）前后暂停与恢复保活。
func (s *imagesFakeStream) pause() {
	s.mu.Lock()
	s.paused = true
	s.mu.Unlock()
}

func (s *imagesFakeStream) resume() {
	s.mu.Lock()
	s.paused = false
	s.mu.Unlock()
}

// stop 结束保活（请求结束时 defer 调用）。
func (s *imagesFakeStream) stop() {
	s.mu.Lock()
	s.finishLocked()
	s.mu.Unlock()
}

// writeEvents 结束保活并写出事件（必要时先提交 SSE 响应头）。
func (s *imagesFakeStream) writeEvents(events []service.ImagesStreamEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishLocked()
	if s.dead {
		return
	}
	s.commitLocked()
	for _, event := range events {
		if err := writeImagesSSEEvent(s.c.Writer, event.Name, event.Payload); err != nil {
			s.dead = true
			return
		}
	}
	s.c.Writer.Flush()
}

// failIfCommitted 结束保活；SSE 已提交时把错误写成 error 事件并返回 true，
// 否则返回 false（调用方按普通 JSON 错误响应）。检查与结束在同一把锁内，
// 不会与保活 goroutine 的提交交错。
func (s *imagesFakeStream) failIfCommitted(errType, code, message string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishLocked()
	if !s.started {
		return false
	}
	if !s.dead {
		if err := writeImagesSSEEvent(s.c.Writer, "error", service.ImagesStreamErrorPayload(errType, code, message)); err != nil {
			s.dead = true
		} else {
			s.c.Writer.Flush()
		}
	}
	return true
}

// imageStreamKeepaliveInterval 是假流式的 SSE 保活间隔
// （gateway.image_stream_keepalive_interval，默认 10 秒；0 关闭）。
func (h *GatewayHandler) imageStreamKeepaliveInterval() time.Duration {
	if h.cfg == nil || h.cfg.Gateway.ImageStreamKeepaliveInterval <= 0 {
		return 0
	}
	return time.Duration(h.cfg.Gateway.ImageStreamKeepaliveInterval) * time.Second
}

// imageNonstreamKeepaliveInterval 是非流式 JSON 空白保活间隔
// （gateway.image_nonstream_keepalive_interval，默认 0 关闭）。
func (h *GatewayHandler) imageNonstreamKeepaliveInterval() time.Duration {
	if h.cfg == nil || h.cfg.Gateway.ImageNonstreamKeepaliveInterval <= 0 {
		return 0
	}
	return time.Duration(h.cfg.Gateway.ImageNonstreamKeepaliveInterval) * time.Second
}

// imageReleaseOnce 把槽位释放函数包成幂等的；不随请求 ctx 取消而提前释放
// （出图提交后上游与客户端连接脱钩，断开即释放会让用户绕过并发上限）。
func imageReleaseOnce(release func()) func() {
	if release == nil {
		return nil
	}
	var once sync.Once
	return func() { once.Do(release) }
}

// openAIErrorTypeForStatus 按状态码给出 OpenAI 错误对象的 type。
func openAIErrorTypeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		return "api_error"
	}
}

func writeImagesSSEEvent(w io.Writer, name string, payload []byte) error {
	if name != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", name); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "data: %s\n\n", payload)
	return err
}
