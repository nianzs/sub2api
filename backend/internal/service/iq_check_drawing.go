package service

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// The drawing is for people to look at; it never affects the IQ check verdict.
const iqCheckDrawingPrompt = "用一个独立的 SVG 画一只骑自行车的鹈鹕，用 SVG 自带的动画（SMIL 或 <style> 里的 CSS 动画）让车轮转动、双腿踩踏、背景向后移动。不要使用 <script>、外部图片、字体或任何外部资源，不要调用任何工具。只输出 SVG 代码，从 <svg 开始到 </svg> 结束，不要解释，不要 Markdown 代码块。"

const iqCheckSVGMaxBytes = 256 << 10

// IQCheckDrawing is the latest drawing of one account, stored apart from
// account Extra so account lists do not carry the SVG.
type IQCheckDrawing struct {
	AccountID int64     `json:"account_id"`
	SVG       string    `json:"svg"`
	Error     string    `json:"error"`
	Model     string    `json:"model"`
	LatencyMs int64     `json:"latency_ms"`
	DrawnAt   time.Time `json:"drawn_at"`
}

type IQCheckDrawingRepository interface {
	// Save replaces the account's previous drawing.
	Save(ctx context.Context, drawing *IQCheckDrawing) error
	ListByAccountIDs(ctx context.Context, ids []int64) ([]IQCheckDrawing, error)
	// DeleteForDeletedAccounts drops drawings of soft-deleted accounts, which
	// the table's ON DELETE CASCADE never sees.
	DeleteForDeletedAccounts(ctx context.Context) (int64, error)
}

var (
	iqSVGRootTag       = regexp.MustCompile(`(?is)^<svg\b[^>]*>`)
	iqSVGNamespace     = regexp.MustCompile(`(?i)\sxmlns\s*=`)
	iqSVGXlinkNamespce = regexp.MustCompile(`(?i)\sxmlns:xlink\s*=`)
)

// extractIQCheckSVG returns the <svg>…</svg> part of the model output. An SVG
// shown through <img> must declare its namespace, so a missing one is added.
func extractIQCheckSVG(output string) (string, bool) {
	lower := strings.ToLower(output)
	start, end := strings.Index(lower, "<svg"), strings.LastIndex(lower, "</svg>")
	if start < 0 || end < start {
		return "", false
	}
	svg := output[start : end+len("</svg>")]
	root := iqSVGRootTag.FindString(svg)
	if root == "" || len(svg) > iqCheckSVGMaxBytes {
		return "", false
	}
	var attrs string
	if !iqSVGNamespace.MatchString(root) {
		attrs += ` xmlns="http://www.w3.org/2000/svg"`
	}
	if strings.Contains(svg, "xlink:") && !iqSVGXlinkNamespce.MatchString(root) {
		attrs += ` xmlns:xlink="http://www.w3.org/1999/xlink"`
	}
	// Keep the tag name as written: XML matches it against the closing tag.
	return svg[:len("<svg")] + attrs + svg[len("<svg"):], true
}

// drawPelican asks the account for the drawing. Failures are recorded, not judged.
func (s *IQCheckService) drawPelican(ctx context.Context, accountID int64, model string) *IQCheckDrawing {
	started := time.Now()
	drawCtx, cancel := context.WithTimeout(ctx, iqCheckQuestionTimeout)
	defer cancel()
	output, err := s.probe(drawCtx, accountID, model, iqCheckDrawingPrompt)
	drawing := &IQCheckDrawing{AccountID: accountID, Model: model, LatencyMs: time.Since(started).Milliseconds(), DrawnAt: s.now()}
	switch svg, ok := extractIQCheckSVG(output); {
	case err != nil:
		drawing.Error = truncateIQCheckError(err.Error())
	case !ok:
		drawing.Error = "model did not return a usable SVG"
	default:
		drawing.SVG = svg
	}
	return drawing
}

func (s *IQCheckService) saveDrawing(ctx context.Context, drawing *IQCheckDrawing) {
	if drawing == nil || s.drawings == nil {
		return
	}
	if err := s.drawings.Save(ctx, drawing); err != nil {
		logger.LegacyPrintf("service.iq_check", "save drawing failed: account=%d err=%v", drawing.AccountID, err)
	}
}

// ListDrawings returns the latest drawings of the given accounts.
func (s *IQCheckService) ListDrawings(ctx context.Context, ids []int64) ([]IQCheckDrawing, error) {
	if s == nil || s.drawings == nil || len(ids) == 0 {
		return []IQCheckDrawing{}, nil
	}
	return s.drawings.ListByAccountIDs(ctx, ids)
}
