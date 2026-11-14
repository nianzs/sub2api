package service

import (
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// LeonardoImagesStreamEvents 把一次 Leonardo 出图结果翻译成 OpenAI Images 流式
// 协议的 completed 事件（每张图一条，事件名 image_generation.completed /
// image_edit.completed）。Leonardo 上游不流式：这是「假流式」，没有
// partial_image 事件（OpenAI 允许实际预览张数少于请求的 partial_images）。
// usage 是按积分估算的整次请求用量（result.Usage），只挂在最后一个 completed
// 事件上：n>1 时每个事件都带会让按事件累加的客户端算成 n 倍。
//
// 产物经对象存储转存时只有 url：b64_json 留空、url 填存储地址。
func LeonardoImagesStreamEvents(req *OpenAIImagesRequest, result *LeonardoImageResult) []ImagesStreamEvent {
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
		var usage []byte
		if i == len(items)-1 {
			usage = result.Usage
		}
		payload := buildOpenAIImagesStreamCompletedPayload(eventName, openAIResponsesImageResult{
			Result:       b64,
			OutputFormat: result.OutputFormat,
			Size:         result.Size,
			Background:   result.Background,
			Quality:      result.Quality,
			Model:        model,
		}, responseFormat, createdAt, usage)
		if b64 == "" {
			payload, _ = sjson.SetBytes(payload, "url", strings.TrimSpace(item.Get("url").String()))
		}
		events = append(events, ImagesStreamEvent{Name: eventName, Payload: payload})
	}
	return events
}
