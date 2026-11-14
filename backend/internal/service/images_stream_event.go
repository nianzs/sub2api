package service

import (
	"strings"

	"github.com/tidwall/sjson"
)

// ImagesStreamEvent 是一条 OpenAI Images 流式（SSE）事件。
type ImagesStreamEvent struct {
	Name    string
	Payload []byte
}

// ImagesStreamErrorPayload 构造流式 error 事件的 data（{"type":"error","error":{…}}）。
// 与 OpenAI 错误对象一致，param / code 两个键始终存在（无值时为 null）。
func ImagesStreamErrorPayload(errType, code, message string) []byte {
	payload := buildOpenAIImagesStreamErrorBody(message)
	if errType = strings.TrimSpace(errType); errType != "" {
		payload, _ = sjson.SetBytes(payload, "error.type", errType)
	}
	payload, _ = sjson.SetRawBytes(payload, "error.param", []byte("null"))
	if code = strings.TrimSpace(code); code != "" {
		payload, _ = sjson.SetBytes(payload, "error.code", code)
	} else {
		payload, _ = sjson.SetRawBytes(payload, "error.code", []byte("null"))
	}
	return payload
}
