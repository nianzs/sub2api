package service

import (
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// FishImagesStreamEvents 把一次 fish 出图结果翻译成 OpenAI Images 流式协议
// 的 completed 事件（每张图一条）。fish 上游不流式：这是「假流式」，没有
// partial_image 事件（OpenAI 允许实际预览张数少于请求的 partial_images）。
func FishImagesStreamEvents(req *OpenAIImagesRequest, result *FishImageResult) []ImagesStreamEvent {
	if result == nil {
		return nil
	}
	eventName := openAIImagesStreamPrefix(req) + ".completed"
	responseFormat := ""
	model := ""
	if req != nil {
		responseFormat = req.ResponseFormat
		model = strings.TrimSpace(req.Model)
	}
	createdAt := gjson.GetBytes(result.Body, "created").Int()
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}

	var items []gjson.Result
	for _, item := range gjson.GetBytes(result.Body, "data").Array() {
		if strings.TrimSpace(item.Get("b64_json").String()) != "" || strings.TrimSpace(item.Get("url").String()) != "" {
			items = append(items, item)
		}
	}
	events := make([]ImagesStreamEvent, 0, len(items))
	for i, item := range items {
		b64 := strings.TrimSpace(item.Get("b64_json").String())
		// usage 是整次请求的估算用量，只挂在最后一个 completed 事件上：n>1 时
		// 每个事件都带会让按事件累加的客户端算成 n 倍。
		var usage []byte
		if i == len(items)-1 {
			usage = result.Usage
		}
		payload := buildOpenAIImagesStreamCompletedPayload(eventName, openAIResponsesImageResult{
			Result:  b64,
			Size:    result.Size,
			Quality: result.Quality,
			Model:   model,
		}, responseFormat, createdAt, usage)
		if b64 == "" {
			payload, _ = sjson.SetBytes(payload, "url", strings.TrimSpace(item.Get("url").String()))
		}
		events = append(events, ImagesStreamEvent{Name: eventName, Payload: payload})
	}
	return events
}
