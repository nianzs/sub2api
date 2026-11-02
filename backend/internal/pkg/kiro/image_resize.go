package kiro

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/jpeg"
	"log/slog"
	"math"
	"net/http"
	"strings"

	"golang.org/x/image/draw"
)

// 图片缩小参数，与 kiro.rs image_resize.rs 的默认值一致。长边上限复用
// kiroImageTokenLongEdge（1568，Anthropic 推荐值）。
const (
	// kiroImageMaxBase64Len 是单张图片 base64 的预算，留在 Kiro 单字段上限之下。
	kiroImageMaxBase64Len   = 400_000
	kiroImageJPEGQuality    = 85
	kiroImageMinJPEGQuality = 35
	kiroImageMinLongSide    = 256
	// kiroImageMaxDecodePixels 限制本地解码的像素数（约 200MB RGBA），
	// 防止超大图片把进程内存打满；超过时原样发送，交给上游判断。
	kiroImageMaxDecodePixels = 50_000_000
)

// shrinkKiroImage 在发给 Kiro 之前把过大的图片缩小，规则与 kiro.rs 的
// maybe_shrink_image 一致：
//   - base64 不超过 kiroImageMaxBase64Len 且长边不超过 1568px：原样发送；
//   - GIF 原样发送（可能是动图）；
//   - 其余等比缩到长边 1568px 并转成 JPEG，仍超预算就逐步降质量、再缩尺寸。
//
// Kiro 上游对单个字段有硬上限，一张原尺寸的手机截图就会触发
// CONTENT_LENGTH_EXCEEDS_THRESHOLD。原样发送时按文件头纠正格式标注，避免
// 上游的 MIME 校验报错。解码或编码失败时原样发送，不丢图、不让请求失败。
func shrinkKiroImage(img KiroImage) KiroImage {
	data := strings.TrimSpace(img.Source.Bytes)
	raw, ok := decodeKiroImageBase64(data)
	if !ok {
		return img
	}
	if actual := detectKiroImageFormat(raw); actual != "" {
		img.Format = actual
	}

	cfg, _, cfgErr := image.DecodeConfig(bytes.NewReader(raw))
	if cfgErr == nil && len(data) <= kiroImageMaxBase64Len && max(cfg.Width, cfg.Height) <= kiroImageTokenLongEdge {
		return img
	}
	if img.Format == "gif" {
		return img
	}
	if cfgErr == nil && cfg.Width*cfg.Height > kiroImageMaxDecodePixels {
		slog.Warn("kiro: image too large to resize locally, sending original",
			"format", img.Format, "width", cfg.Width, "height", cfg.Height, "base64_bytes", len(data))
		return img
	}

	out, err := encodeShrunkKiroImage(raw)
	if err != nil {
		slog.Warn("kiro: image resize failed, sending original",
			"format", img.Format, "base64_bytes", len(data), "error", err)
		return img
	}
	return KiroImage{
		Format: "jpeg",
		Source: KiroImageSource{Bytes: base64.StdEncoding.EncodeToString(out)},
	}
}

func decodeKiroImageBase64(data string) ([]byte, bool) {
	if data == "" {
		return nil, false
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		if raw, err := encoding.DecodeString(data); err == nil && len(raw) > 0 {
			return raw, true
		}
	}
	return nil, false
}

// detectKiroImageFormat 按文件头识别真实格式，识别不了返回空串（保留原标注）。
func detectKiroImageFormat(raw []byte) string {
	switch http.DetectContentType(raw) {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpeg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	default:
		return ""
	}
}

// encodeShrunkKiroImage 解码后按长边上限缩放并编码成 JPEG。先在当前尺寸下
// 逐级降质量（每次 -10，最低 35）；最低质量仍超预算时把长边缩到 0.8 倍再试，
// 直到长边 256px 为止。
func encodeShrunkKiroImage(raw []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	bounds := src.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return nil, errors.New("image has no pixels")
	}

	longSide := min(max(bounds.Dx(), bounds.Dy()), kiroImageTokenLongEdge)
	var buf bytes.Buffer
	for {
		frame := scaleKiroImageOntoWhite(src, longSide)
		quality := kiroImageJPEGQuality
		for {
			buf.Reset()
			if err := jpeg.Encode(&buf, frame, &jpeg.Options{Quality: quality}); err != nil {
				return nil, err
			}
			if kiroImageFitsBudget(buf.Len()) || quality <= kiroImageMinJPEGQuality {
				break
			}
			quality = max(quality-10, kiroImageMinJPEGQuality)
		}
		if kiroImageFitsBudget(buf.Len()) || longSide <= kiroImageMinLongSide {
			return buf.Bytes(), nil
		}
		longSide = max(int(float64(longSide)*0.8), kiroImageMinLongSide)
	}
}

func kiroImageFitsBudget(rawLen int) bool {
	return base64.StdEncoding.EncodedLen(rawLen) <= kiroImageMaxBase64Len
}

// scaleKiroImageOntoWhite 把图片等比缩到长边不超过 longSide。JPEG 没有透明
// 通道，先铺白底，透明区域不会变成黑色。
func scaleKiroImageOntoWhite(src image.Image, longSide int) *image.RGBA {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if longest := max(width, height); longest > longSide {
		scale := float64(longSide) / float64(longest)
		width = max(1, int(math.Round(float64(width)*scale)))
		height = max(1, int(math.Round(float64(height)*scale)))
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)
	return dst
}
