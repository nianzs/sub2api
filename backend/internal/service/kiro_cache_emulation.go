package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropictokenizer"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

const (
	kiroCacheDefaultTTL          = 5 * time.Minute
	kiroCacheOneHourTTL          = time.Hour
	kiroCacheMaxSupportedTTL     = time.Hour
	kiroTokensPerTool            = 150
	kiroTokensPerMessage         = 4
	kiroCacheMinTokensDefault    = 1024
	kiroCacheMinTokensOpus       = 4096
	kiroCacheMinTokensHaiku3     = 2048
	kiroCachePrefixLookbackLimit = 10
)

type kiroCacheEmulationUsage struct {
	InputTokens                int
	CacheReadInputTokens       int
	CacheCreationInputTokens   int
	CacheCreation5mInputTokens int
	CacheCreation1hInputTokens int
}

type kiroCacheEntry struct {
	tokens    int
	ttl       time.Duration
	expiresAt time.Time
}

type kiroCacheTracker struct {
	mu      sync.Mutex
	entries map[uint64]map[[32]byte]kiroCacheEntry
}

var globalKiroCacheTracker = &kiroCacheTracker{entries: make(map[uint64]map[[32]byte]kiroCacheEntry)}

// kiroCacheEmulationPlan 把缓存估算拆成"计算"与"落盘"两步：compute() 只读,
// commit() 才会把本次前缀写入 tracker。调用方应在确认上游请求成功后再 commit(),
// 避免请求失败/未发出时就把内容错误标记为已缓存。
type kiroCacheEmulationPlan struct {
	usage    *kiroCacheEmulationUsage
	cacheKey uint64
	profile  *kiroCacheProfile
}

func (p *kiroCacheEmulationPlan) result() *kiroCacheEmulationUsage {
	if p == nil {
		return nil
	}
	return p.usage
}

func (p *kiroCacheEmulationPlan) commit() {
	if p == nil || p.profile == nil || p.cacheKey == 0 {
		return
	}
	globalKiroCacheTracker.update(p.cacheKey, p.profile)
}

func (s *GatewayService) buildKiroCacheEmulationUsage(ctx context.Context, account *Account, group *Group, body []byte, model string, inputTokens int) *kiroCacheEmulationUsage {
	plan := s.prepareKiroCacheEmulationUsage(ctx, account, group, body, model, inputTokens)
	plan.commit()
	return plan.result()
}

func (s *GatewayService) prepareKiroCacheEmulationUsage(ctx context.Context, account *Account, group *Group, body []byte, model string, inputTokens int) *kiroCacheEmulationPlan {
	NormalizeGroupRuntimeFields(group)
	if group == nil || !group.EffectiveKiroCacheEmulationEnabled() || account == nil || account.ID <= 0 || len(body) == 0 {
		return nil
	}
	profile, ok := buildKiroCacheProfile(ctx, body, model, inputTokens)
	if !ok {
		return nil
	}
	return s.prepareKiroCacheEmulationPlanFromProfile(account, group, profile, inputTokens)
}

func (s *GatewayService) buildKiroResponsesCacheEmulationUsage(ctx context.Context, account *Account, group *Group, body []byte, model string, inputTokens int) *kiroCacheEmulationUsage {
	plan := s.prepareKiroResponsesCacheEmulationUsage(ctx, account, group, body, model, inputTokens)
	plan.commit()
	return plan.result()
}

func (s *GatewayService) prepareKiroResponsesCacheEmulationUsage(ctx context.Context, account *Account, group *Group, body []byte, model string, inputTokens int) *kiroCacheEmulationPlan {
	NormalizeGroupRuntimeFields(group)
	if group == nil || !group.EffectiveKiroCacheEmulationEnabled() || account == nil || account.ID <= 0 || len(body) == 0 {
		return nil
	}
	profile, ok := buildKiroResponsesCacheProfile(ctx, body, model, inputTokens)
	if !ok {
		return nil
	}
	return s.prepareKiroCacheEmulationPlanFromProfile(account, group, profile, inputTokens)
}

func (s *GatewayService) buildKiroChatCompletionsCacheEmulationUsage(ctx context.Context, account *Account, group *Group, body []byte, model string, inputTokens int) *kiroCacheEmulationUsage {
	plan := s.prepareKiroChatCompletionsCacheEmulationUsage(ctx, account, group, body, model, inputTokens)
	plan.commit()
	return plan.result()
}

func (s *GatewayService) prepareKiroChatCompletionsCacheEmulationUsage(ctx context.Context, account *Account, group *Group, body []byte, model string, inputTokens int) *kiroCacheEmulationPlan {
	NormalizeGroupRuntimeFields(group)
	if group == nil || !group.EffectiveKiroCacheEmulationEnabled() || account == nil || account.ID <= 0 || len(body) == 0 {
		return nil
	}
	profile, ok := buildKiroChatCompletionsCacheProfile(ctx, body, model, inputTokens)
	if !ok {
		return nil
	}
	effectiveInputTokens := inputTokens
	if effectiveInputTokens <= 0 {
		effectiveInputTokens = profile.totalInputTokens
	}
	return s.prepareKiroCacheEmulationPlanFromProfile(account, group, profile, effectiveInputTokens)
}

func (s *GatewayService) prepareKiroCacheEmulationPlanFromProfile(account *Account, group *Group, profile *kiroCacheProfile, inputTokens int) *kiroCacheEmulationPlan {
	if group == nil || account == nil || account.ID <= 0 || profile == nil {
		return nil
	}
	cacheKey := kiroCacheCredentialKey(account)
	if cacheKey == 0 {
		return nil
	}
	result := globalKiroCacheTracker.compute(cacheKey, profile)
	ratio := group.EffectiveKiroCacheEmulationRatio()
	result.CacheReadInputTokens = scaleKiroCacheTokens(result.CacheReadInputTokens, ratio)
	result.CacheCreationInputTokens = scaleKiroCacheTokens(result.CacheCreationInputTokens, ratio)
	result.CacheCreation5mInputTokens = scaleKiroCacheTokens(result.CacheCreation5mInputTokens, ratio)
	result.CacheCreation1hInputTokens = scaleKiroCacheTokens(result.CacheCreation1hInputTokens, ratio)
	result.InputTokens = inputTokens - result.CacheReadInputTokens - result.CacheCreationInputTokens
	if result.InputTokens < 0 {
		result.InputTokens = 0
	}
	if result.CacheReadInputTokens == 0 && result.CacheCreationInputTokens == 0 {
		result = nil
	}
	return &kiroCacheEmulationPlan{usage: result, cacheKey: cacheKey, profile: profile}
}

func scaleKiroCacheTokens(tokens int, ratio float64) int {
	if tokens <= 0 || ratio <= 0 {
		return 0
	}
	if ratio >= 1 {
		return tokens
	}
	return int(math.Round(float64(tokens) * ratio))
}

type kiroCacheProfile struct {
	totalInputTokens              int
	minCacheable                  int
	scaleBreakpointsToInputTokens bool
	blocks                        []kiroCacheBlock
	breakpoints                   []kiroCacheBreakpoint
}

type kiroCacheBlock struct {
	prefixFingerprint [32]byte
	cumulativeTokens  int
}

type kiroCacheBreakpoint struct {
	blockIndex int
	ttl        time.Duration
}

type kiroResolvedBreakpoint struct {
	blockIndex       int
	cumulativeTokens int
	ttl              time.Duration
}

type kiroPendingBlock struct {
	value         any
	tokens        int
	breakpointTTL *time.Duration
	messageIndex  *int
	isMessageEnd  bool
}

func buildKiroCacheProfile(ctx context.Context, body []byte, model string, inputTokens int) (*kiroCacheProfile, bool) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false
	}
	blocks := flattenKiroCacheBlocks(ctx, payload)
	if len(blocks) == 0 {
		return nil, false
	}
	totalTokens := inputTokens
	if totalTokens <= 0 {
		totalTokens = countKiroInputTokensFromPayload(ctx, payload)
	}
	prelude := map[string]any{
		"model":       payload["model"],
		"tool_choice": payload["tool_choice"],
	}
	return buildKiroCacheProfileFromBlocks(model, totalTokens, prelude, blocks)
}

func buildKiroResponsesCacheProfile(ctx context.Context, body []byte, model string, inputTokens int) (*kiroCacheProfile, bool) {
	var req apicompat.ResponsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, false
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false
	}
	blocks := flattenKiroResponsesCacheBlocks(ctx, payload)
	if len(blocks) == 0 {
		return nil, false
	}
	ttl := kiroCacheDefaultTTL
	applyKiroResponsesDefaultBreakpoints(blocks, ttl)

	effectiveTools, err := apicompat.EffectiveResponsesTools(&req)
	if err != nil {
		return nil, false
	}
	effectiveModel := strings.TrimSpace(model)
	if effectiveModel == "" {
		effectiveModel, _ = payload["model"].(string)
	}
	totalTokens := inputTokens
	if totalTokens <= 0 {
		totalTokens = countKiroResponsesInputTokens(payload, blocks, len(effectiveTools))
	}
	prelude := map[string]any{
		"protocol":             "responses",
		"model":                effectiveModel,
		"tool_choice":          payload["tool_choice"],
		"tools":                kiroJSONCompatibleValue(effectiveTools),
		"prompt_cache_key":     payload["prompt_cache_key"],
		"previous_response_id": payload["previous_response_id"],
		"reasoning_effort":     kiroNestedValue(payload, "reasoning", "effort"),
		"text_format":          kiroNestedValue(payload, "text", "format"),
	}
	profile, ok := buildKiroCacheProfileFromBlocks(effectiveModel, totalTokens, prelude, blocks)
	if ok {
		profile.scaleBreakpointsToInputTokens = true
	}
	return profile, ok
}

func buildKiroChatCompletionsCacheProfile(ctx context.Context, body []byte, model string, inputTokens int) (*kiroCacheProfile, bool) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false
	}
	blocks := flattenKiroChatCompletionsCacheBlocks(ctx, payload)
	if len(blocks) == 0 {
		return nil, false
	}
	applyKiroResponsesDefaultBreakpoints(blocks, kiroCacheDefaultTTL)

	effectiveModel := strings.TrimSpace(model)
	if effectiveModel == "" {
		effectiveModel, _ = payload["model"].(string)
	}
	tools, _ := payload["tools"].([]any)
	functions, _ := payload["functions"].([]any)
	totalTokens := inputTokens
	if totalTokens <= 0 {
		totalTokens = countKiroChatCompletionsInputTokens(payload, blocks, len(tools)+len(functions))
	}
	prelude := map[string]any{
		"protocol":            "chat_completions",
		"model":               effectiveModel,
		"instructions":        payload["instructions"],
		"tool_choice":         payload["tool_choice"],
		"function_call":       payload["function_call"],
		"tools":               kiroJSONCompatibleValue(payload["tools"]),
		"functions":           kiroJSONCompatibleValue(payload["functions"]),
		"parallel_tool_calls": payload["parallel_tool_calls"],
		"reasoning_effort":    payload["reasoning_effort"],
		"response_format":     kiroJSONCompatibleValue(payload["response_format"]),
	}
	profile, ok := buildKiroCacheProfileFromBlocks(effectiveModel, totalTokens, prelude, blocks)
	if ok {
		profile.scaleBreakpointsToInputTokens = true
	}
	return profile, ok
}

func buildKiroCacheProfileFromBlocks(model string, totalTokens int, preludeValue any, blocks []kiroPendingBlock) (*kiroCacheProfile, bool) {
	prelude, err := canonicalJSON(preludeValue)
	if err != nil {
		return nil, false
	}
	prefixState := make([]byte, 8+len(prelude))
	binary.BigEndian.PutUint64(prefixState[:8], uint64(len(prelude)))
	copy(prefixState[8:], prelude)

	profile := &kiroCacheProfile{totalInputTokens: max(totalTokens, 0), minCacheable: kiroMinimumCacheableTokens(model)}
	cumulativeTokens := 0
	var activeTTL *time.Duration
	seenBreakpoints := make(map[int]struct{})
	for index, block := range blocks {
		cumulativeTokens += max(block.tokens, 0)
		blockJSON, err := canonicalJSON(block.value)
		if err != nil {
			return nil, false
		}
		blockHash := sha256.Sum256(blockJSON)
		h := sha256.New()
		_, _ = h.Write(prefixState)
		_, _ = h.Write(blockHash[:])
		prefixFingerprint := [32]byte(h.Sum(nil))
		prefixState = prefixFingerprint[:]
		profile.blocks = append(profile.blocks, kiroCacheBlock{prefixFingerprint: prefixFingerprint, cumulativeTokens: cumulativeTokens})

		if block.breakpointTTL != nil {
			ttl := minDuration(*block.breakpointTTL, kiroCacheMaxSupportedTTL)
			activeTTL = &ttl
			if _, ok := seenBreakpoints[index]; !ok {
				profile.breakpoints = append(profile.breakpoints, kiroCacheBreakpoint{blockIndex: index, ttl: ttl})
				seenBreakpoints[index] = struct{}{}
			}
		}
		if block.isMessageEnd && block.messageIndex != nil && activeTTL != nil {
			if _, ok := seenBreakpoints[index]; !ok {
				profile.breakpoints = append(profile.breakpoints, kiroCacheBreakpoint{blockIndex: index, ttl: *activeTTL})
				seenBreakpoints[index] = struct{}{}
			}
		}
	}
	if profile.lastCacheableBreakpoint() == nil {
		return nil, false
	}
	return profile, true
}

func flattenKiroCacheBlocks(ctx context.Context, payload map[string]any) []kiroPendingBlock {
	var blocks []kiroPendingBlock
	if tools, ok := payload["tools"].([]any); ok {
		for toolIndex, tool := range tools {
			value := stripKiroCacheControl(tool)
			blocks = append(blocks, kiroPendingBlock{
				value:  map[string]any{"kind": "tool", "tool_index": toolIndex, "tool": value},
				tokens: kiroTokensPerTool, breakpointTTL: extractKiroCacheTTL(tool),
			})
		}
	}
	for systemIndex, systemBlock := range normalizeKiroSystemBlocks(payload["system"]) {
		value := stripKiroCacheControl(systemBlock)
		canonicalizeKiroSystemBlock(value)
		blocks = append(blocks, kiroPendingBlock{
			value:  map[string]any{"kind": "system", "system_index": systemIndex, "block": value},
			tokens: countKiroSystemBlockTokens(systemBlock), breakpointTTL: extractKiroCacheTTL(systemBlock),
		})
	}
	messages, _ := payload["messages"].([]any)
	for messageIndex, rawMessage := range messages {
		message, _ := rawMessage.(map[string]any)
		role, _ := message["role"].(string)
		content := message["content"]
		switch typed := content.(type) {
		case string:
			mi := messageIndex
			block := map[string]any{"type": "text", "text": typed}
			blocks = append(blocks, kiroPendingBlock{
				value:  map[string]any{"kind": "message", "message_index": messageIndex, "role": role, "block_index": 0, "block": block},
				tokens: countKiroMessageContentTokens(ctx, block), messageIndex: &mi, isMessageEnd: true,
			})
		case []any:
			lastBlockIndex := len(typed) - 1
			for blockIndex, rawBlock := range typed {
				mi := messageIndex
				value := stripKiroCacheControl(rawBlock)
				blocks = append(blocks, kiroPendingBlock{
					value:  map[string]any{"kind": "message", "message_index": messageIndex, "role": role, "block_index": blockIndex, "block": value},
					tokens: countKiroMessageContentTokens(ctx, rawBlock), breakpointTTL: extractKiroCacheTTL(rawBlock), messageIndex: &mi, isMessageEnd: blockIndex == lastBlockIndex,
				})
			}
		}
	}
	return blocks
}

func flattenKiroResponsesCacheBlocks(ctx context.Context, payload map[string]any) []kiroPendingBlock {
	var blocks []kiroPendingBlock
	if instructions, ok := payload["instructions"].(string); strings.TrimSpace(instructions) != "" && ok {
		block := map[string]any{"type": "input_text", "text": strings.TrimSpace(instructions)}
		blocks = append(blocks, kiroPendingBlock{
			value:        map[string]any{"kind": "instructions", "block": block},
			tokens:       countKiroMessageContentTokens(ctx, block),
			isMessageEnd: true,
		})
	}

	switch input := payload["input"].(type) {
	case string:
		block := map[string]any{"type": "input_text", "text": input}
		blocks = append(blocks, kiroPendingBlock{
			value:        map[string]any{"kind": "input", "input_index": 0, "role": "user", "block_index": 0, "block": block},
			tokens:       countKiroMessageContentTokens(ctx, block),
			isMessageEnd: true,
		})
	case []any:
		cacheableItemCount := countKiroResponsesCacheableInputItems(input)
		seenCacheableItems := 0
		for itemIndex, rawItem := range input {
			if !isKiroResponsesCacheableInputItem(rawItem) {
				blocks = appendKiroResponsesInputItemBlocks(ctx, blocks, itemIndex, rawItem, false)
				continue
			}
			seenCacheableItems++
			isStableHistoryItem := seenCacheableItems < cacheableItemCount
			blocks = appendKiroResponsesInputItemBlocks(ctx, blocks, itemIndex, rawItem, isStableHistoryItem)
		}
	}
	return blocks
}

func flattenKiroChatCompletionsCacheBlocks(ctx context.Context, payload map[string]any) []kiroPendingBlock {
	var blocks []kiroPendingBlock
	if instructions, ok := payload["instructions"].(string); ok && strings.TrimSpace(instructions) != "" {
		block := map[string]any{"type": "input_text", "text": strings.TrimSpace(instructions)}
		blocks = append(blocks, kiroPendingBlock{
			value:  map[string]any{"kind": "instructions", "block": block},
			tokens: countKiroMessageContentTokens(ctx, block),
		})
	}

	messages, _ := payload["messages"].([]any)
	for messageIndex, rawMessage := range messages {
		markMessageEnd := isKiroChatCompletionsCacheableMessage(rawMessage)
		blocks = appendKiroChatCompletionsMessageBlocks(ctx, blocks, messageIndex, rawMessage, markMessageEnd)
	}
	return blocks
}

func isKiroChatCompletionsCacheableMessage(rawMessage any) bool {
	message, ok := rawMessage.(map[string]any)
	if !ok {
		return false
	}
	role, _ := message["role"].(string)
	return strings.TrimSpace(role) != ""
}

func appendKiroChatCompletionsMessageBlocks(ctx context.Context, blocks []kiroPendingBlock, messageIndex int, rawMessage any, markMessageEnd bool) []kiroPendingBlock {
	start := len(blocks)
	message, ok := rawMessage.(map[string]any)
	if !ok {
		return blocks
	}
	role, _ := message["role"].(string)
	name, _ := message["name"].(string)

	if content, ok := message["content"]; ok {
		blocks = appendKiroChatCompletionsContentBlocks(ctx, blocks, messageIndex, role, name, content)
	}
	if reasoning, ok := message["reasoning_content"].(string); ok && reasoning != "" {
		block := map[string]any{"type": "reasoning", "thinking": reasoning}
		blocks = append(blocks, kiroPendingBlock{
			value:  map[string]any{"kind": "message_reasoning", "message_index": messageIndex, "role": role, "name": name, "block": block},
			tokens: countKiroMessageContentTokens(ctx, block),
		})
	}
	if toolCalls, ok := message["tool_calls"]; ok {
		value := kiroJSONCompatibleValue(toolCalls)
		blocks = append(blocks, kiroPendingBlock{
			value:  map[string]any{"kind": "message_tool_calls", "message_index": messageIndex, "role": role, "name": name, "block": value},
			tokens: countKiroSerializedValueTokens(value),
		})
	}
	if toolCallID, ok := message["tool_call_id"].(string); ok && toolCallID != "" {
		value := map[string]any{"tool_call_id": toolCallID}
		blocks = append(blocks, kiroPendingBlock{
			value:  map[string]any{"kind": "message_tool_call_id", "message_index": messageIndex, "role": role, "name": name, "block": value},
			tokens: countKiroSerializedValueTokens(value),
		})
	}
	if functionCall, ok := message["function_call"]; ok {
		value := kiroJSONCompatibleValue(functionCall)
		blocks = append(blocks, kiroPendingBlock{
			value:  map[string]any{"kind": "message_function_call", "message_index": messageIndex, "role": role, "name": name, "block": value},
			tokens: countKiroSerializedValueTokens(value),
		})
	}
	return markKiroResponsesInputItemEnd(blocks, start, messageIndex, markMessageEnd)
}

func appendKiroChatCompletionsContentBlocks(ctx context.Context, blocks []kiroPendingBlock, messageIndex int, role string, name string, content any) []kiroPendingBlock {
	switch typed := content.(type) {
	case string:
		block := map[string]any{"type": "text", "text": typed}
		return append(blocks, kiroPendingBlock{
			value:  map[string]any{"kind": "message", "message_index": messageIndex, "role": role, "name": name, "block_index": 0, "block": block},
			tokens: countKiroMessageContentTokens(ctx, block),
		})
	case []any:
		for blockIndex, rawBlock := range typed {
			block := rawBlock
			if text, ok := rawBlock.(string); ok {
				block = map[string]any{"type": "text", "text": text}
			}
			blocks = append(blocks, kiroPendingBlock{
				value:  map[string]any{"kind": "message", "message_index": messageIndex, "role": role, "name": name, "block_index": blockIndex, "block": kiroJSONCompatibleValue(block)},
				tokens: countKiroMessageContentTokens(ctx, block),
			})
		}
	case map[string]any:
		blocks = append(blocks, kiroPendingBlock{
			value:  map[string]any{"kind": "message", "message_index": messageIndex, "role": role, "name": name, "block_index": 0, "block": kiroJSONCompatibleValue(typed)},
			tokens: countKiroMessageContentTokens(ctx, typed),
		})
	}
	return blocks
}

func applyKiroResponsesDefaultBreakpoints(blocks []kiroPendingBlock, ttl time.Duration) {
	if len(blocks) == 0 {
		return
	}
	for i := range blocks {
		if blocks[i].isMessageEnd {
			blocks[i].breakpointTTL = &ttl
		}
	}
	blocks[len(blocks)-1].breakpointTTL = &ttl
}

func countKiroResponsesCacheableInputItems(input []any) int {
	count := 0
	for _, rawItem := range input {
		if isKiroResponsesCacheableInputItem(rawItem) {
			count++
		}
	}
	return count
}

func isKiroResponsesCacheableInputItem(rawItem any) bool {
	if _, ok := rawItem.(string); ok {
		return true
	}
	item, ok := rawItem.(map[string]any)
	if !ok {
		return false
	}
	itemType, _ := item["type"].(string)
	return itemType != "additional_tools"
}

func appendKiroResponsesInputItemBlocks(ctx context.Context, blocks []kiroPendingBlock, itemIndex int, rawItem any, markMessageEnd bool) []kiroPendingBlock {
	start := len(blocks)
	if text, ok := rawItem.(string); ok {
		block := map[string]any{"type": "input_text", "text": text}
		blocks = append(blocks, kiroPendingBlock{
			value:  map[string]any{"kind": "input", "input_index": itemIndex, "role": "user", "block_index": 0, "block": block},
			tokens: countKiroMessageContentTokens(ctx, block),
		})
		return markKiroResponsesInputItemEnd(blocks, start, itemIndex, markMessageEnd)
	}
	item, ok := rawItem.(map[string]any)
	if !ok {
		return blocks
	}
	itemType, _ := item["type"].(string)
	if itemType == "additional_tools" {
		return blocks
	}
	role, _ := item["role"].(string)
	if role == "" && (itemType == "message" || itemType == "") {
		role = "user"
	}
	if content, ok := item["content"]; ok {
		blocks = appendKiroResponsesContentBlocks(ctx, blocks, itemIndex, role, content)
		return markKiroResponsesInputItemEnd(blocks, start, itemIndex, markMessageEnd)
	}
	value := kiroJSONCompatibleValue(item)
	blocks = append(blocks, kiroPendingBlock{
		value:  map[string]any{"kind": "input_item", "input_index": itemIndex, "type": itemType, "block": value},
		tokens: countKiroSerializedValueTokens(value),
	})
	return markKiroResponsesInputItemEnd(blocks, start, itemIndex, markMessageEnd)
}

func markKiroResponsesInputItemEnd(blocks []kiroPendingBlock, start, itemIndex int, markMessageEnd bool) []kiroPendingBlock {
	if !markMessageEnd || len(blocks) <= start {
		return blocks
	}
	last := len(blocks) - 1
	mi := itemIndex
	blocks[last].messageIndex = &mi
	blocks[last].isMessageEnd = true
	return blocks
}

func appendKiroResponsesContentBlocks(ctx context.Context, blocks []kiroPendingBlock, itemIndex int, role string, content any) []kiroPendingBlock {
	switch typed := content.(type) {
	case string:
		block := map[string]any{"type": "input_text", "text": typed}
		return append(blocks, kiroPendingBlock{
			value:  map[string]any{"kind": "input", "input_index": itemIndex, "role": role, "block_index": 0, "block": block},
			tokens: countKiroMessageContentTokens(ctx, block),
		})
	case []any:
		for blockIndex, rawBlock := range typed {
			block := rawBlock
			if text, ok := rawBlock.(string); ok {
				block = map[string]any{"type": "input_text", "text": text}
			}
			blocks = append(blocks, kiroPendingBlock{
				value:  map[string]any{"kind": "input", "input_index": itemIndex, "role": role, "block_index": blockIndex, "block": kiroJSONCompatibleValue(block)},
				tokens: countKiroMessageContentTokens(ctx, block),
			})
		}
	case map[string]any:
		blocks = append(blocks, kiroPendingBlock{
			value:  map[string]any{"kind": "input", "input_index": itemIndex, "role": role, "block_index": 0, "block": kiroJSONCompatibleValue(typed)},
			tokens: countKiroMessageContentTokens(ctx, typed),
		})
	}
	return blocks
}

func countKiroResponsesInputTokens(payload map[string]any, blocks []kiroPendingBlock, toolCount int) int {
	tokens := toolCount * kiroTokensPerTool
	for _, block := range blocks {
		tokens += max(block.tokens, 0)
	}
	if input, ok := payload["input"].([]any); ok {
		tokens += len(input) * kiroTokensPerMessage
	}
	return max(tokens, 1)
}

func countKiroChatCompletionsInputTokens(payload map[string]any, blocks []kiroPendingBlock, toolCount int) int {
	tokens := toolCount * kiroTokensPerTool
	for _, block := range blocks {
		tokens += max(block.tokens, 0)
	}
	if messages, ok := payload["messages"].([]any); ok {
		tokens += len(messages) * kiroTokensPerMessage
	}
	return max(tokens, 1)
}

func kiroNestedValue(payload map[string]any, keys ...string) any {
	var current any = payload
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = m[key]
	}
	return current
}

func kiroJSONCompatibleValue(value any) any {
	b, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return value
	}
	return out
}

func normalizeKiroSystemBlocks(system any) []any {
	switch typed := system.(type) {
	case nil:
		return nil
	case string:
		return []any{map[string]any{"type": "text", "text": typed}}
	case []any:
		return typed
	default:
		return []any{typed}
	}
}

func canonicalizeKiroSystemBlock(value any) {
	obj, ok := value.(map[string]any)
	if !ok {
		return
	}
	blockType, _ := obj["type"].(string)
	if blockType != "" && blockType != "text" {
		return
	}
	text, _ := obj["text"].(string)
	if strings.HasPrefix(text, "x-anthropic-billing-header:") {
		obj["text"] = "__anthropic_billing_header__"
	}
}

func extractKiroCacheTTL(value any) *time.Duration {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	cc, ok := obj["cache_control"].(map[string]any)
	if !ok || !strings.EqualFold(strings.TrimSpace(kiroCacheAsString(cc["type"])), "ephemeral") {
		return nil
	}
	ttl := kiroCacheDefaultTTL
	if strings.EqualFold(strings.TrimSpace(kiroCacheAsString(cc["ttl"])), "1h") {
		ttl = kiroCacheOneHourTTL
	}
	return &ttl
}

func (p *kiroCacheProfile) cacheableBreakpoints() []kiroResolvedBreakpoint {
	if p == nil {
		return nil
	}
	resolved := make([]kiroResolvedBreakpoint, 0, len(p.breakpoints))
	for _, breakpoint := range p.breakpoints {
		if breakpoint.blockIndex < 0 || breakpoint.blockIndex >= len(p.blocks) {
			continue
		}
		block := p.blocks[breakpoint.blockIndex]
		if block.cumulativeTokens < p.minCacheable {
			continue
		}
		resolved = append(resolved, kiroResolvedBreakpoint{blockIndex: breakpoint.blockIndex, cumulativeTokens: block.cumulativeTokens, ttl: breakpoint.ttl})
	}
	return resolved
}

func (p *kiroCacheProfile) lastCacheableBreakpoint() *kiroResolvedBreakpoint {
	breakpoints := p.cacheableBreakpoints()
	if len(breakpoints) == 0 {
		return nil
	}
	last := breakpoints[len(breakpoints)-1]
	return &last
}

func (t *kiroCacheTracker) compute(cacheKey uint64, profile *kiroCacheProfile) *kiroCacheEmulationUsage {
	out := &kiroCacheEmulationUsage{}
	if t == nil || profile == nil || cacheKey == 0 {
		return out
	}
	lastBreakpoint := profile.lastCacheableBreakpoint()
	if lastBreakpoint == nil {
		return out
	}
	lastBreakpointTokens := profile.cacheTokensForBreakpoint(lastBreakpoint.cumulativeTokens)
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(now)

	matchedTokens := 0
	if accountEntries := t.entries[cacheKey]; accountEntries != nil {
		breakpoints := profile.cacheableBreakpoints()
		for i, seen := len(breakpoints)-1, 0; i >= 0 && seen < kiroCachePrefixLookbackLimit; i, seen = i-1, seen+1 {
			breakpoint := breakpoints[i]
			candidate := profile.blocks[breakpoint.blockIndex]
			entry, ok := accountEntries[candidate.prefixFingerprint]
			if !ok || !entry.expiresAt.After(now) {
				continue
			}
			entry.expiresAt = now.Add(entry.ttl)
			accountEntries[candidate.prefixFingerprint] = entry
			matchedTokens = profile.cacheTokensForBreakpoint(breakpoint.cumulativeTokens)
			break
		}
	}
	newTokens := max(lastBreakpointTokens-matchedTokens, 0)
	out.CacheReadInputTokens = max(matchedTokens, 0)
	out.CacheCreationInputTokens = newTokens
	out.CacheCreation5mInputTokens, out.CacheCreation1hInputTokens = profile.ttlBreakdown(matchedTokens)
	return out
}

func (p *kiroCacheProfile) cacheTokensForBreakpoint(cumulativeTokens int) int {
	if p == nil {
		return 0
	}
	if !p.scaleBreakpointsToInputTokens {
		return min(max(cumulativeTokens, 0), p.totalInputTokens)
	}
	lastBreakpoint := p.lastCacheableBreakpoint()
	if lastBreakpoint == nil || lastBreakpoint.cumulativeTokens <= 0 {
		return min(max(cumulativeTokens, 0), p.totalInputTokens)
	}
	scaled := int(math.Round(float64(max(cumulativeTokens, 0)) * float64(p.totalInputTokens) / float64(lastBreakpoint.cumulativeTokens)))
	return min(max(scaled, 0), p.totalInputTokens)
}

func (p *kiroCacheProfile) ttlBreakdown(matchedTokens int) (int, int) {
	lastBreakpoint := p.lastCacheableBreakpoint()
	if lastBreakpoint == nil {
		return 0, 0
	}
	newTokens := max(p.cacheTokensForBreakpoint(lastBreakpoint.cumulativeTokens)-matchedTokens, 0)
	if newTokens == 0 {
		return 0, 0
	}
	if lastBreakpoint.ttl >= kiroCacheOneHourTTL {
		return 0, newTokens
	}
	return newTokens, 0
}

func (t *kiroCacheTracker) update(cacheKey uint64, profile *kiroCacheProfile) {
	if t == nil || profile == nil || cacheKey == 0 {
		return
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(now)
	accountEntries := t.entries[cacheKey]
	if accountEntries == nil {
		accountEntries = make(map[[32]byte]kiroCacheEntry)
		t.entries[cacheKey] = accountEntries
	}
	for _, breakpoint := range profile.cacheableBreakpoints() {
		block := profile.blocks[breakpoint.blockIndex]
		expiresAt := now.Add(breakpoint.ttl)
		entry, ok := accountEntries[block.prefixFingerprint]
		if ok {
			entry.tokens = max(entry.tokens, block.cumulativeTokens)
			entry.ttl = maxDuration(entry.ttl, breakpoint.ttl)
			if expiresAt.After(entry.expiresAt) {
				entry.expiresAt = expiresAt
			}
			accountEntries[block.prefixFingerprint] = entry
			continue
		}
		accountEntries[block.prefixFingerprint] = kiroCacheEntry{tokens: block.cumulativeTokens, ttl: breakpoint.ttl, expiresAt: expiresAt}
	}
}

func (t *kiroCacheTracker) pruneLocked(now time.Time) {
	for cacheKey, accountEntries := range t.entries {
		for fp, entry := range accountEntries {
			if !entry.expiresAt.After(now) {
				delete(accountEntries, fp)
			}
		}
		if len(accountEntries) == 0 {
			delete(t.entries, cacheKey)
		}
	}
}

func kiroCacheCredentialKey(account *Account) uint64 {
	stableKey := strings.TrimSpace(kiroCacheCredentialIdentity(account))
	if stableKey == "" {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(stableKey))
	return h.Sum64()
}

func kiroCacheCredentialIdentity(account *Account) string {
	if account == nil {
		return ""
	}
	parts := make([]string, 0, 8)
	for _, key := range []string{"client_id_hash", "client_id", "refresh_token", "profile_arn", "kiro_api_key", "kiroApiKey", "api_key"} {
		if value := strings.TrimSpace(account.GetCredential(key)); value != "" {
			parts = append(parts, key+":"+value)
		}
	}
	if len(parts) == 0 && account.ID > 0 {
		parts = append(parts, "account:"+fmt.Sprint(account.ID))
	}
	return strings.Join(parts, "|")
}

func kiroMinimumCacheableTokens(model string) int {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus"):
		return kiroCacheMinTokensOpus
	case strings.Contains(m, "haiku-3") || strings.Contains(m, "haiku_3"):
		return kiroCacheMinTokensHaiku3
	default:
		return kiroCacheMinTokensDefault
	}
}

func stripKiroCacheControl(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, child := range x {
			if k == "cache_control" {
				continue
			}
			out[k] = stripKiroCacheControl(child)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, child := range x {
			out[i] = stripKiroCacheControl(child)
		}
		return out
	default:
		return v
	}
}

func countKiroInputTokensFromPayload(ctx context.Context, payload map[string]any) int {
	if payload == nil {
		return 1
	}
	tokens := 0
	for _, block := range normalizeKiroSystemBlocks(payload["system"]) {
		tokens += countKiroSystemBlockTokens(block)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) > 0 {
		sanitizedMessages, imageTokens := sanitizeKiroImagesForTokenEstimate(ctx, messages)
		canonical, err := canonicalJSON(sanitizedMessages)
		if err == nil {
			tokens += anthropictokenizer.CountTokens(string(canonical))
		}
		tokens += imageTokens
		tokens += len(messages) * kiroTokensPerMessage
	}
	if tools, ok := payload["tools"].([]any); ok {
		tokens += len(tools) * kiroTokensPerTool
	}
	return max(tokens, 1)
}

func countKiroSystemBlockTokens(value any) int {
	switch typed := value.(type) {
	case string:
		return anthropictokenizer.CountTokens(typed)
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			return anthropictokenizer.CountTokens(text)
		}
		return 0
	default:
		return 0
	}
}

func countKiroMessageContentTokens(ctx context.Context, value any) int {
	switch typed := value.(type) {
	case nil:
		return 0
	case string:
		return anthropictokenizer.CountTokens(typed)
	case []any:
		total := 0
		for _, item := range typed {
			total += countKiroMessageContentTokens(ctx, item)
		}
		return total
	case map[string]any:
		if mediaType, source, ok := kiroImageTokenSource(typed); ok {
			return kiropkg.EstimateImageTokens(ctx, mediaType, source)
		}
		if text, ok := typed["text"].(string); ok {
			return anthropictokenizer.CountTokens(text)
		}
		if thinking, ok := typed["thinking"].(string); ok {
			return anthropictokenizer.CountTokens(thinking)
		}
		if input, ok := typed["input"]; ok {
			return countKiroSerializedValueTokens(input)
		}
		if content, ok := typed["content"]; ok {
			return countKiroMessageContentTokens(ctx, content)
		}
		return 0
	default:
		return 0
	}
}

func sanitizeKiroImagesForTokenEstimate(ctx context.Context, value any) (any, int) {
	switch typed := value.(type) {
	case []any:
		out := make([]any, len(typed))
		tokens := 0
		for i, item := range typed {
			out[i], tokens = sanitizeKiroImageItem(ctx, item, tokens)
		}
		return out, tokens
	case map[string]any:
		if mediaType, source, ok := kiroImageTokenSource(typed); ok {
			return sanitizeKiroImageBlock(typed), kiropkg.EstimateImageTokens(ctx, mediaType, source)
		}
		out := make(map[string]any, len(typed))
		tokens := 0
		for key, item := range typed {
			out[key], tokens = sanitizeKiroImageItem(ctx, item, tokens)
		}
		return out, tokens
	default:
		return value, 0
	}
}

func sanitizeKiroImageItem(ctx context.Context, value any, currentTokens int) (any, int) {
	sanitized, tokens := sanitizeKiroImagesForTokenEstimate(ctx, value)
	return sanitized, currentTokens + tokens
}

func sanitizeKiroImageBlock(value map[string]any) map[string]any {
	out := make(map[string]any, len(value))
	for key, item := range value {
		switch typed := item.(type) {
		case map[string]any:
			out[key] = sanitizeKiroImageBlock(typed)
		case []any:
			items := make([]any, len(typed))
			for i, child := range typed {
				if childMap, ok := child.(map[string]any); ok {
					items[i] = sanitizeKiroImageBlock(childMap)
				} else {
					items[i] = child
				}
			}
			out[key] = items
		case string:
			lowerKey := strings.ToLower(key)
			if lowerKey == "data" || lowerKey == "url" || lowerKey == "image_url" {
				out[key] = "[image]"
			} else {
				out[key] = typed
			}
		default:
			out[key] = item
		}
	}
	return out
}

func kiroImageTokenSource(value map[string]any) (mediaType, source string, ok bool) {
	kind, _ := value["type"].(string)
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "image":
		mediaType, source = kiroImageSourceFields(value)
		return mediaType, source, true
	case "image_url", "input_image":
		mediaType, source = kiroImageSourceFields(value)
		if raw, exists := value["image_url"]; exists {
			switch typed := raw.(type) {
			case string:
				source = typed
			case map[string]any:
				if url, found := typed["url"].(string); found {
					source = url
				}
			}
		}
		return mediaType, source, true
	default:
		return "", "", false
	}
}

func kiroImageSourceFields(value map[string]any) (mediaType, source string) {
	container := value
	if nested, ok := value["source"].(map[string]any); ok {
		container = nested
	}
	for _, key := range []string{"media_type", "mediaType", "mime_type"} {
		if candidate, ok := container[key].(string); ok && strings.TrimSpace(candidate) != "" {
			mediaType = candidate
			break
		}
	}
	for _, key := range []string{"data", "url"} {
		if candidate, ok := container[key].(string); ok && strings.TrimSpace(candidate) != "" {
			source = candidate
			break
		}
	}
	return mediaType, source
}

func countKiroSerializedValueTokens(value any) int {
	canonical, err := canonicalJSON(value)
	if err != nil {
		return 0
	}
	return anthropictokenizer.CountTokens(string(canonical))
}

func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonicalJSON(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonicalJSON(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		_ = buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				_ = buf.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			_, _ = buf.Write(kb)
			_ = buf.WriteByte(':')
			if err := writeCanonicalJSON(buf, x[k]); err != nil {
				return err
			}
		}
		_ = buf.WriteByte('}')
		return nil
	case []any:
		_ = buf.WriteByte('[')
		for i, child := range x {
			if i > 0 {
				_ = buf.WriteByte(',')
			}
			if err := writeCanonicalJSON(buf, child); err != nil {
				return err
			}
		}
		_ = buf.WriteByte(']')
		return nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, _ = buf.Write(b)
		return nil
	}
}

func kiroCacheAsString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func (u *kiroCacheEmulationUsage) toKiroUsage() *kiropkg.Usage {
	if u == nil {
		return nil
	}
	return &kiropkg.Usage{
		InputTokens:                u.InputTokens,
		CacheReadInputTokens:       u.CacheReadInputTokens,
		CacheCreationInputTokens:   u.CacheCreationInputTokens,
		CacheCreation5mInputTokens: u.CacheCreation5mInputTokens,
		CacheCreation1hInputTokens: u.CacheCreation1hInputTokens,
	}
}
