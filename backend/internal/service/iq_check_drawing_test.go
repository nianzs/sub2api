//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractIQCheckSVG(t *testing.T) {
	svg, ok := extractIQCheckSVG("好的：\n```svg\n<svg viewBox=\"0 0 10 10\"><circle r=\"4\"/></svg>\n```")
	require.True(t, ok)
	require.Equal(t, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><circle r="4"/></svg>`, svg)

	svg, ok = extractIQCheckSVG(`<svg xmlns="http://www.w3.org/2000/svg"><use xlink:href="#a"/></svg>`)
	require.True(t, ok)
	require.Equal(t, `<svg xmlns:xlink="http://www.w3.org/1999/xlink" xmlns="http://www.w3.org/2000/svg"><use xlink:href="#a"/></svg>`, svg)

	kept := `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="#a"/></svg>`
	svg, ok = extractIQCheckSVG(kept)
	require.True(t, ok)
	require.Equal(t, kept, svg)

	for _, output := range []string{"", "no drawing here", "<svg><g>", "<svgx></svg>",
		"<svg>" + strings.Repeat("a", iqCheckSVGMaxBytes) + "</svg>"} {
		_, ok = extractIQCheckSVG(output)
		require.False(t, ok, output[:min(len(output), 20)])
	}
}

type iqCheckTestDrawings struct {
	mu     sync.Mutex
	saved  []IQCheckDrawing
	prunes int
}

func (d *iqCheckTestDrawings) DeleteForDeletedAccounts(context.Context) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prunes++
	return 0, nil
}

func (d *iqCheckTestDrawings) Save(_ context.Context, drawing *IQCheckDrawing) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.saved = append(d.saved, *drawing)
	return nil
}

func (d *iqCheckTestDrawings) ListByAccountIDs(context.Context, []int64) ([]IQCheckDrawing, error) {
	return nil, nil
}

func TestIQCheckDrawsBesideTheQuestionsWithoutAffectingTheVerdict(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))
	drawings := &iqCheckTestDrawings{}
	h.svc.drawings = drawings
	answer := h.svc.probe
	drawingErr := error(nil)
	h.svc.probe = func(ctx context.Context, id int64, model, prompt string) (string, error) {
		if prompt == iqCheckDrawingPrompt {
			if drawingErr != nil {
				return "", drawingErr
			}
			return `<svg viewBox="0 0 4 4"><rect width="4" height="4"/></svg>`, nil
		}
		return answer(ctx, id, model, prompt)
	}

	h.round("right")
	require.Len(t, drawings.saved, 1)
	require.Contains(t, drawings.saved[0].SVG, `xmlns="http://www.w3.org/2000/svg"`)
	require.Empty(t, drawings.saved[0].Error)
	require.Equal(t, int64(1), drawings.saved[0].AccountID)
	require.NotEmpty(t, drawings.saved[0].Model)

	// A failed drawing is recorded and does not change the verdict.
	drawingErr = errors.New("upstream 429: rate limited")
	h.round("right")
	require.Len(t, drawings.saved, 2)
	require.Empty(t, drawings.saved[1].SVG)
	require.Equal(t, "upstream 429: rate limited", drawings.saved[1].Error)
	require.Equal(t, IQCheckResultPassed, h.state(t).Result)
	require.Equal(t, 4, h.state(t).AnsweredCorrect)
}

func TestIQCheckListDrawingsWithoutStorage(t *testing.T) {
	var missing *IQCheckService
	drawings, err := missing.ListDrawings(context.Background(), []int64{1})
	require.NoError(t, err)
	require.Empty(t, drawings)
}
