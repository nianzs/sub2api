package kiro

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func encodeTestPNG(t *testing.T, img image.Image) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func gradientImage(width, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: uint8(x + y), A: 255})
		}
	}
	return img
}

func noiseImage(width, height int) *image.NRGBA {
	rng := rand.New(rand.NewSource(1))
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	_, _ = rng.Read(img.Pix)
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	return img
}

func decodeKiroImageForTest(t *testing.T, img KiroImage) image.Image {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(img.Source.Bytes)
	require.NoError(t, err)
	decoded, _, err := image.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	return decoded
}

func TestShrinkKiroImageKeepsSmallImage(t *testing.T) {
	data := encodeTestPNG(t, gradientImage(200, 100))
	in := KiroImage{Format: "png", Source: KiroImageSource{Bytes: data}}
	require.Equal(t, in, shrinkKiroImage(in))
}

func TestShrinkKiroImageDownscalesLongSide(t *testing.T) {
	data := encodeTestPNG(t, gradientImage(3000, 1000))
	out := shrinkKiroImage(KiroImage{Format: "png", Source: KiroImageSource{Bytes: data}})

	require.Equal(t, "jpeg", out.Format)
	require.LessOrEqual(t, len(out.Source.Bytes), kiroImageMaxBase64Len)
	bounds := decodeKiroImageForTest(t, out).Bounds()
	require.Equal(t, kiroImageTokenLongEdge, bounds.Dx())
	require.Equal(t, 523, bounds.Dy())
}

// 尺寸不大但字节超预算（噪点图压不动）：逐级降质量、再缩尺寸，直到放进预算。
func TestShrinkKiroImageFitsByteBudget(t *testing.T) {
	data := encodeTestPNG(t, noiseImage(1200, 1200))
	require.Greater(t, len(data), kiroImageMaxBase64Len)

	out := shrinkKiroImage(KiroImage{Format: "png", Source: KiroImageSource{Bytes: data}})
	require.Equal(t, "jpeg", out.Format)
	require.LessOrEqual(t, len(out.Source.Bytes), kiroImageMaxBase64Len)
	bounds := decodeKiroImageForTest(t, out).Bounds()
	require.Less(t, bounds.Dx(), 1200)
	require.Equal(t, bounds.Dx(), bounds.Dy())
}

// JPEG 没有透明通道：透明区域铺白底，不能变黑。
func TestShrinkKiroImageFlattensTransparencyOntoWhite(t *testing.T) {
	data := encodeTestPNG(t, image.NewNRGBA(image.Rect(0, 0, 2000, 40)))
	out := shrinkKiroImage(KiroImage{Format: "png", Source: KiroImageSource{Bytes: data}})

	require.Equal(t, "jpeg", out.Format)
	r, g, b, _ := decodeKiroImageForTest(t, out).At(10, 10).RGBA()
	require.Greater(t, r>>8, uint32(240))
	require.Greater(t, g>>8, uint32(240))
	require.Greater(t, b>>8, uint32(240))
}

func TestShrinkKiroImageKeepsGIF(t *testing.T) {
	palette := []color.Color{color.Black, color.White}
	frame := image.NewPaletted(image.Rect(0, 0, 2000, 20), palette)
	var buf bytes.Buffer
	require.NoError(t, gif.Encode(&buf, frame, nil))
	in := KiroImage{Format: "gif", Source: KiroImageSource{Bytes: base64.StdEncoding.EncodeToString(buf.Bytes())}}

	require.Equal(t, in, shrinkKiroImage(in))
}

func TestShrinkKiroImageKeepsUndecodableData(t *testing.T) {
	in := KiroImage{Format: "png", Source: KiroImageSource{Bytes: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("not an image", 50_000)))}}
	require.Equal(t, in, shrinkKiroImage(in))

	notBase64 := KiroImage{Format: "png", Source: KiroImageSource{Bytes: "%%%"}}
	require.Equal(t, notBase64, shrinkKiroImage(notBase64))
}

// 客户端标错格式（PNG 标成 jpeg）时按文件头纠正，避免上游 MIME 校验报错。
func TestShrinkKiroImageCorrectsMislabeledFormat(t *testing.T) {
	data := encodeTestPNG(t, gradientImage(50, 50))
	out := shrinkKiroImage(KiroImage{Format: "jpeg", Source: KiroImageSource{Bytes: data}})
	require.Equal(t, "png", out.Format)
	require.Equal(t, data, out.Source.Bytes)
}

func TestBuildKiroPayloadShrinksLargeImage(t *testing.T) {
	data := encodeTestPNG(t, gradientImage(2600, 1200))
	body, err := json.Marshal(map[string]any{
		"model": "claude-sonnet-4-5",
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "look"},
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": data}},
		}}},
	})
	require.NoError(t, err)

	built, err := BuildKiroPayloadWithContext(body, "claude-sonnet-4.5", "", "AI_EDITOR", nil)
	require.NoError(t, err)
	img := gjson.GetBytes(built.Payload, "conversationState.currentMessage.userInputMessage.images.0")
	require.Equal(t, "jpeg", img.Get("format").String())
	raw, err := base64.StdEncoding.DecodeString(img.Get("source.bytes").String())
	require.NoError(t, err)
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	require.NoError(t, err)
	require.Equal(t, kiroImageTokenLongEdge, cfg.Width)
}

// 历史里重复出现的同一张截图只发第一次，之后换成说明；当前消息里的图片总是保留。
func TestBuildKiroPayloadDedupsRepeatedHistoryImages(t *testing.T) {
	same := encodeTestPNG(t, gradientImage(40, 40))
	other := encodeTestPNG(t, gradientImage(41, 40))
	userImage := func(text, data string) map[string]any {
		return map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": text},
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": data}},
		}}
	}
	asst := func(text string) map[string]any { return map[string]any{"role": "assistant", "content": text} }
	body, err := json.Marshal(map[string]any{
		"model": "claude-sonnet-4-5",
		"messages": []any{
			userImage("first", same), asst("a1"),
			userImage("again", same), asst("a2"),
			userImage("different", other), asst("a3"),
			userImage("current", same),
		},
	})
	require.NoError(t, err)

	built, err := BuildKiroPayloadWithContext(body, "claude-sonnet-4.5", "", "AI_EDITOR", nil)
	require.NoError(t, err)

	userMsg := func(text string) gjson.Result {
		return gjson.GetBytes(built.Payload, `conversationState.history.#(userInputMessage.content%"`+text+`*").userInputMessage`)
	}
	first := userMsg("first")
	require.Equal(t, same, first.Get("images.0.source.bytes").String())

	again := userMsg("again")
	require.Empty(t, again.Get("images").Array())
	require.Equal(t, "again\n\n"+kiroDuplicateHistoryImagePlaceholder, again.Get("content").String())

	require.Equal(t, other, userMsg("different").Get("images.0.source.bytes").String())

	current := gjson.GetBytes(built.Payload, "conversationState.currentMessage.userInputMessage")
	require.Equal(t, same, current.Get("images.0.source.bytes").String())
	require.NotContains(t, current.Get("content").String(), kiroDuplicateHistoryImagePlaceholder)
}

// 历史里重复的远程图片 URL 不会再次抓取。
func TestBuildKiroPayloadDedupsRepeatedHistoryImageURLs(t *testing.T) {
	pngBytes, err := base64.StdEncoding.DecodeString(encodeTestPNG(t, gradientImage(8, 8)))
	require.NoError(t, err)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer server.Close()

	remote := func(text string) map[string]any {
		return map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": text},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": server.URL + "/shot.png"}},
		}}
	}
	body, err := json.Marshal(map[string]any{
		"model": "claude-sonnet-4-5",
		"messages": []any{
			remote("first"), map[string]any{"role": "assistant", "content": "a1"},
			remote("again"), map[string]any{"role": "assistant", "content": "a2"},
			map[string]any{"role": "user", "content": "current"},
		},
	})
	require.NoError(t, err)

	built, err := BuildKiroPayloadWithContext(body, "claude-sonnet-4.5", "", "AI_EDITOR", nil)
	require.NoError(t, err)
	require.Equal(t, int32(1), requests.Load())
	require.Equal(t, 1, strings.Count(string(built.Payload), kiroDuplicateHistoryImagePlaceholder))
}

func toolResultImageMessage(id string, blocks ...any) map[string]any {
	return map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": id, "content": blocks},
	}}
}

func toolUseMessage(id string) map[string]any {
	return map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "tool_use", "id": id, "name": "screenshot", "input": map[string]any{}},
	}}
}

func base64ImageBlock(data string) map[string]any {
	return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": data}}
}

// tool_result 里的图片提到当前消息顶层 images（Kiro 的 toolResult 没有图片字段），
// 走同一条压缩链路；只有图片时文本为 [image attached]。
func TestBuildKiroPayloadLiftsToolResultImages(t *testing.T) {
	big := encodeTestPNG(t, gradientImage(2600, 1200))
	body, err := json.Marshal(map[string]any{
		"model": "claude-sonnet-4-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "take a screenshot"},
			toolUseMessage("toolu_1"),
			toolResultImageMessage("toolu_1", base64ImageBlock(big)),
		},
	})
	require.NoError(t, err)

	built, err := BuildKiroPayloadWithContext(body, "claude-sonnet-4.5", "", "AI_EDITOR", nil)
	require.NoError(t, err)
	current := gjson.GetBytes(built.Payload, "conversationState.currentMessage.userInputMessage")
	require.Equal(t, "jpeg", current.Get("images.0.format").String())
	result := current.Get("userInputMessageContext.toolResults.0")
	require.Equal(t, "toolu_1", result.Get("toolUseId").String())
	require.Equal(t, kiroToolResultImageOnlyText, result.Get("content.0.text").String())
}

// 图文混合时文本照旧保留，图片同样提到顶层。
func TestBuildKiroPayloadLiftsToolResultImageKeepsText(t *testing.T) {
	small := encodeTestPNG(t, gradientImage(20, 20))
	body, err := json.Marshal(map[string]any{
		"model": "claude-sonnet-4-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "read the image"},
			toolUseMessage("toolu_1"),
			toolResultImageMessage("toolu_1", map[string]any{"type": "text", "text": "file: a.png"}, base64ImageBlock(small)),
		},
	})
	require.NoError(t, err)

	built, err := BuildKiroPayloadWithContext(body, "claude-sonnet-4.5", "", "AI_EDITOR", nil)
	require.NoError(t, err)
	current := gjson.GetBytes(built.Payload, "conversationState.currentMessage.userInputMessage")
	require.Equal(t, small, current.Get("images.0.source.bytes").String())
	texts := current.Get("userInputMessageContext.toolResults.0.content.#.text").Array()
	require.Len(t, texts, 1)
	require.Equal(t, "file: a.png", texts[0].String())
}

// 历史里两次相同的工具截图只发第一张，第二次的 tool_result 文本换成去重说明。
func TestBuildKiroPayloadDedupsToolResultImagesInHistory(t *testing.T) {
	same := encodeTestPNG(t, gradientImage(30, 30))
	body, err := json.Marshal(map[string]any{
		"model": "claude-sonnet-4-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "watch the screen"},
			toolUseMessage("toolu_1"),
			toolResultImageMessage("toolu_1", base64ImageBlock(same)),
			toolUseMessage("toolu_2"),
			toolResultImageMessage("toolu_2", base64ImageBlock(same)),
			map[string]any{"role": "assistant", "content": "nothing changed"},
			map[string]any{"role": "user", "content": "ok"},
		},
	})
	require.NoError(t, err)

	built, err := BuildKiroPayloadWithContext(body, "claude-sonnet-4.5", "", "AI_EDITOR", nil)
	require.NoError(t, err)
	byToolResult := func(id string) gjson.Result {
		return gjson.GetBytes(built.Payload, `conversationState.history.#(userInputMessage.userInputMessageContext.toolResults.0.toolUseId=="`+id+`").userInputMessage`)
	}
	first := byToolResult("toolu_1")
	require.Equal(t, same, first.Get("images.0.source.bytes").String())
	require.Equal(t, kiroToolResultImageOnlyText, first.Get("userInputMessageContext.toolResults.0.content.0.text").String())

	second := byToolResult("toolu_2")
	require.Empty(t, second.Get("images").Array())
	require.Equal(t, kiroDuplicateHistoryImagePlaceholder, second.Get("userInputMessageContext.toolResults.0.content.0.text").String())
	require.Equal(t, 1, strings.Count(string(built.Payload), same))
}
