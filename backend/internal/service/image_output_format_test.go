package service

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/stretchr/testify/require"
)

// testImage 生成一张 w×h 的渐变图（有足够细节让 JPEG 质量差异体现在体积上）。
func testImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 7), uint8(y * 11), uint8((x ^ y) * 5), 255})
		}
	}
	return img
}

func encodeTestJPEG(t *testing.T, w, h, quality int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, testImage(w, h), &jpeg.Options{Quality: quality}))
	return buf.Bytes()
}

func encodeTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, testImage(w, h)))
	return buf.Bytes()
}

func TestNormalizeImageOutputFormat(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"":       {"", true},
		"png":    {"png", true},
		" PNG ":  {"png", true},
		"jpeg":   {"jpeg", true},
		"JPG":    {"jpeg", true},
		"webp":   {"", false},
		"bmp":    {"", false},
		"png8":   {"", false},
		"image/": {"", false},
	}
	for raw, tc := range cases {
		got, ok := normalizeImageOutputFormat(raw)
		require.Equal(t, tc.want, got, raw)
		require.Equal(t, tc.ok, ok, raw)
	}
	require.True(t, isImageOutputFormatWebP(" WEBP "))
	require.False(t, isImageOutputFormatWebP("png"))
}

func TestTranscodeImageJPEGToPNG(t *testing.T) {
	src := encodeTestJPEG(t, 48, 32, 90)
	out, err := transcodeImage(src, imageOutputFormatPNG, nil)
	require.NoError(t, err)
	require.Equal(t, imageOutputFormatPNG, sniffImageOutputFormat(out))
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	require.NoError(t, err)
	require.Equal(t, "png", format)
	require.Equal(t, 48, cfg.Width)
	require.Equal(t, 32, cfg.Height)
}

func TestTranscodeImageKeepsMatchingFormat(t *testing.T) {
	pngSrc := encodeTestPNG(t, 16, 16)
	out, err := transcodeImage(pngSrc, imageOutputFormatPNG, intPtr(10))
	require.NoError(t, err)
	require.Equal(t, pngSrc, out, "png → png 原样返回（output_compression 对 png 无效）")

	jpegSrc := encodeTestJPEG(t, 16, 16, 90)
	for _, compression := range []*int{nil, intPtr(100), intPtr(150)} {
		out, err := transcodeImage(jpegSrc, imageOutputFormatJPEG, compression)
		require.NoError(t, err)
		require.Equal(t, jpegSrc, out, "jpeg → jpeg 且无需降质时原样返回")
	}

	out, err = transcodeImage(jpegSrc, "", nil)
	require.NoError(t, err)
	require.Equal(t, jpegSrc, out, "未指定格式时保持上游格式")
}

func TestTranscodeImageRecompressesJPEG(t *testing.T) {
	src := encodeTestJPEG(t, 128, 128, 100)
	out, err := transcodeImage(src, imageOutputFormatJPEG, intPtr(30))
	require.NoError(t, err)
	require.Equal(t, imageOutputFormatJPEG, sniffImageOutputFormat(out))
	require.Less(t, len(out), len(src), "output_compression=30 应该重新压缩、体积变小")
}

func TestTranscodeImagePNGToJPEG(t *testing.T) {
	out, err := transcodeImage(encodeTestPNG(t, 24, 24), imageOutputFormatJPEG, nil)
	require.NoError(t, err)
	require.Equal(t, imageOutputFormatJPEG, sniffImageOutputFormat(out))
}

func TestApplyImageOutputFormatKeepsOriginalOnFailure(t *testing.T) {
	broken := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 32)...)
	good := encodeTestJPEG(t, 16, 16, 90)

	_, err := transcodeImage(broken, imageOutputFormatPNG, nil)
	require.Error(t, err)

	out := applyImageOutputFormat([][]byte{broken, good}, imageOutputFormatPNG, nil)
	require.Len(t, out, 2)
	require.Equal(t, broken, out[0], "转码失败保留原图")
	require.Equal(t, imageOutputFormatPNG, sniffImageOutputFormat(out[1]))

	same := [][]byte{good}
	require.Equal(t, same, applyImageOutputFormat(same, "", nil))
}
