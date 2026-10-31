import { getProviderProfile } from '@/constants/platformCatalog'
import { DEFAULT_EXCEL_BPS_MODELS } from '@/constants/account'

// =====================
// 模型列表（硬编码，与 new-api 一致）
// =====================

// OpenAI
const openaiModels = [
  // GPT-5.2 系列
  'gpt-5.2', 'gpt-5.2-2025-12-11', 'gpt-5.2-chat-latest',
  'gpt-5.2-pro', 'gpt-5.2-pro-2025-12-11',
	// GPT-5.6 系列
  'gpt-5.6', 'gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna',
  // GPT-6 系列
  'gpt-6', 'gpt-6-astra', 'gpt-6.1-sol', 'gpt-6.1-terra', 'gpt-6-sol', 'gpt-6-luna',
  // GPT-5.5 系列
  'gpt-5.5',
  // GPT-5.4 系列
  'gpt-5.4', 'gpt-5.4-mini', 'gpt-5.4-2026-03-05',
  // GPT-5.3 / Codex 系列
  'gpt-5.3-codex-spark', 'codex-auto-review',
  'gpt-4o-audio-preview', 'gpt-4o-realtime-preview',
  // GPT Image 系列
  'gpt-image-1', 'gpt-image-1.5', 'gpt-image-2', 'gpt-image-2.5-flare', 'gpt-image-2.5-sunburst'
]

// Anthropic Claude
export const claudeModels = [
  'claude-3-5-sonnet-20241022', 'claude-3-5-sonnet-20240620',
  'claude-3-5-haiku-20241022',
  'claude-3-7-sonnet-20250219',
  'claude-sonnet-4-20250514-thinking', 'claude-opus-4-20250514-thinking',
  'claude-sonnet-4-20250514', 'claude-opus-4-20250514',
  'claude-opus-4-1-20250805',
  'claude-sonnet-4-5-thinking', 'claude-sonnet-4-5-20250929-thinking',
  'claude-haiku-4-5-thinking', 'claude-haiku-4-5-20251001-thinking',
  'claude-opus-4-5-thinking', 'claude-opus-4-5-20251101-thinking',
  'claude-sonnet-4-5-20250929', 'claude-haiku-4-5-20251001',
  'claude-opus-4-5-20251101',
  'claude-opus-4-6-thinking',
  'claude-opus-4-6',
  'claude-opus-4-7-thinking',
  'claude-opus-4-7',
  'claude-opus-4-8-thinking',
  'claude-opus-4-8',
  'claude-opus-5-5',
  'claude-opus-5',
  'claude-sonnet-5-5',
  'claude-haiku-5-5',
  'claude-sonnet-5',
  'claude-sonnet-4-6-thinking',
  'claude-sonnet-4-6',
  'claude-fable-5-1',
  'claude-fable-5',
  'claude-2.1', 'claude-2.0', 'claude-instant-1.2'
]

// Google Gemini
const geminiModels = [
  // Keep in sync with backend curated Gemini lists.
  // This list is intentionally conservative (models commonly available across OAuth/API key).
  'gemini-3.1-flash-image',
  'gemini-3.1-flash-image-preview',
  'gemini-3.1-flash-lite-image',
  'gemini-3-pro-image',
  'gemini-3-pro-image-preview',
  'gemini-3-flash',
  'gemini-3-flash-preview',
  'gemini-3.1-pro-preview',
  'gemini-3.1-pro-preview-customtools',
  'gemini-3.1-flash-lite',
  'gemini-3.1-flash-lite-preview',
  'gemini-3.5-flash',
  'gemini-3.5-flash-lite',
  'gemini-3.6-flash',
  'gemini-3.7-flash',
  'gemini-3.8-flash',
  'gemini-4-argon'
]

// Antigravity 官方支持的模型（精确匹配）
// 基于官方 API 返回的模型列表，只支持 Claude 4.5+ 和 Gemini 2.5+
const antigravityModels = [
  // Claude 4.5+ / 5.5 系列
  'claude-fable-5-1',
  'claude-fable-5',
  'claude-opus-5-5-high',
  'claude-opus-5-5-medium',
  'claude-opus-5-5-low',
  'claude-sonnet-5-5-high',
  'claude-sonnet-5-5-medium',
  'claude-sonnet-5-5-low',
  'claude-opus-4-6',
  'claude-opus-4-6-thinking',
  'claude-opus-4-7',
  'claude-opus-4-8',
  'claude-opus-4-5-thinking',
  'claude-sonnet-4-6',
  'claude-sonnet-4-5',
  'claude-sonnet-4-5-thinking',
  // Gemini 2.5 系列
  'gemini-3.1-flash-image',
  'gemini-2.5-flash-image',
  'gemini-2.5-flash',
  'gemini-2.5-flash-lite',
  'gemini-2.5-flash-thinking',
  'gemini-2.5-pro',
  // Gemini 3 系列
  'gemini-3-flash',
  'gemini-3-pro-high',
  'gemini-3-pro-low',
  // Gemini 3.1 系列
  'gemini-3.1-pro',
  'gemini-3.1-pro-high',
  'gemini-3.1-pro-low',
  'gemini-pro-agent',
  'gemini-3-pro-image',
  // Gemini 3.6 / 3.7 / 3.8 Flash 系列
  'gemini-3.6-flash',
  'gemini-3.6-flash-low',
  'gemini-3.6-flash-medium',
  'gemini-3.6-flash-high',
  'gemini-3.6-flash-tiered',
  'gemini-3.7-flash',
  'gemini-3.7-flash-low',
  'gemini-3.7-flash-medium',
  'gemini-3.7-flash-high',
  'gemini-3.7-flash-tiered',
  'gemini-3.8-flash',
  'gemini-3.8-flash-low',
  'gemini-3.8-flash-medium',
  'gemini-3.8-flash-high',
  'gemini-3.8-flash-tiered',
  // Gemini 4 Argon 系列
  'gemini-4-argon-low',
  'gemini-4-argon-medium',
  'gemini-4-argon-high',
  // 其他
  'gpt-oss-120b-medium',
  'tab_flash_lite_preview'
]

// Leonardo 平台对外模型：Cookie 原生号与 API Key 中转号同一口径（后端
// IsModelSupported 强制，映射目标不在此列表的条目不参与调度）。
const leonardoModels = [
  'gpt-image-2',
  'gpt-image-2.5-flare',
  'gpt-image-2.5-sunburst',
]

// Fish Audio 对外模型。与其他平台的目录分开维护。
const fishModels = [
  'gpt-image-2',
  'gpt-image-2.5-flare',
  'gpt-image-2.5-sunburst',
]

const fishPresetMappings = [
  { label: 'GPT Image 2', from: 'gpt-image-2', to: 'gpt-image-2', color: 'bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-400' },
  { label: 'GPT Image 2.5 Flare', from: 'gpt-image-2.5-flare', to: 'gpt-image-2.5-flare', color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-400' },
  { label: 'GPT Image 2.5 Sunburst', from: 'gpt-image-2.5-sunburst', to: 'gpt-image-2.5-sunburst', color: 'bg-amber-100 text-amber-700 hover:bg-amber-200 dark:bg-amber-900/30 dark:text-amber-400' },
]

// Leonardo 预设映射：恒等（Cookie 号出图时后端再换成上游 slug，如 openai/gpt-image-2.5-flare）。
const leonardoPresetMappings = [
  { label: 'GPT Image 2', from: 'gpt-image-2', to: 'gpt-image-2', color: 'bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-400' },
  { label: 'GPT Image 2.5 Flare', from: 'gpt-image-2.5-flare', to: 'gpt-image-2.5-flare', color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-400' },
  { label: 'GPT Image 2.5 Sunburst', from: 'gpt-image-2.5-sunburst', to: 'gpt-image-2.5-sunburst', color: 'bg-amber-100 text-amber-700 hover:bg-amber-200 dark:bg-amber-900/30 dark:text-amber-400' },
]

const kiroModels = [
  'gpt-5.6-sol',
  'gpt-5.6-terra',
  'gpt-5.6-luna',
  'kimi-k3',
  'codex-auto-review',
  'claude-opus-4-8',
  'claude-opus-4-8-thinking',
  'claude-opus-4-7',
  'claude-opus-4-7-thinking',
  'claude-opus-4-6',
  'claude-opus-4-6-thinking',
  'claude-opus-5',
  'claude-opus-5-thinking',
  'claude-opus-5-5',
  'claude-opus-5-5-thinking',
  'claude-sonnet-5',
  'claude-sonnet-5-thinking',
  'claude-sonnet-5-5',
  'claude-sonnet-5-5-thinking',
  'claude-sonnet-4-6',
  'claude-sonnet-4-6-thinking',
  'claude-opus-4-5-20251101',
  'claude-opus-4-5-20251101-thinking',
  'claude-sonnet-4-5-20250929',
  'claude-sonnet-4-5-20250929-thinking',
  'claude-haiku-4-5-20251001',
  'claude-haiku-4-5-20251001-thinking',
  'claude-haiku-5-5',
  'claude-haiku-5-5-thinking'
]

// 智谱 GLM
const zhipuModels = [
  'glm-4', 'glm-4v', 'glm-4-plus', 'glm-4-0520',
  'glm-4-air', 'glm-4-airx', 'glm-4-long', 'glm-4-flash',
  'glm-4v-plus', 'glm-4.5', 'glm-4.5-x', 'glm-4.5-air', 'glm-4.5-flash',
  'glm-4.6', 'glm-4.7', 'glm-4.7-flash', 'glm-4.7-flashx',
  'glm-5', 'glm-5-turbo', 'glm-5.1', 'glm-5.2',
  'glm-5.3', 'glm-5.3-flash',
  'glm-3-turbo', 'glm-4-alltools',
  'chatglm_turbo', 'chatglm_pro', 'chatglm_std', 'chatglm_lite',
  'cogview-3', 'cogvideo'
]

// 阿里 通义千问
const qwenModels = [
  'qwen-turbo', 'qwen-plus', 'qwen-max', 'qwen-max-longcontext', 'qwen-long',
  'qwen2-72b-instruct', 'qwen2-57b-a14b-instruct', 'qwen2-7b-instruct',
  'qwen2.5-72b-instruct', 'qwen2.5-32b-instruct', 'qwen2.5-14b-instruct',
  'qwen2.5-7b-instruct', 'qwen2.5-3b-instruct', 'qwen2.5-1.5b-instruct',
  'qwen2.5-coder-32b-instruct', 'qwen2.5-coder-14b-instruct', 'qwen2.5-coder-7b-instruct',
  'qwen3-235b-a22b',
  'qwq-32b', 'qwq-32b-preview'
]

// DeepSeek
const deepseekModels = [
  'deepseek-v4-pro', 'deepseek-v4-flash', 'deepseek-v4-flash-vision-exp', 'deepseek-flash',
  'deepseek-coder',
  'deepseek-v3', 'deepseek-v3-0324',
  'deepseek-r1', 'deepseek-r1-0528',
  'deepseek-r1-distill-qwen-32b', 'deepseek-r1-distill-qwen-14b', 'deepseek-r1-distill-qwen-7b',
  'deepseek-r1-distill-llama-70b', 'deepseek-r1-distill-llama-8b'
]

// Mistral
const mistralModels = [
  'mistral-small-latest', 'mistral-medium-latest', 'mistral-large-latest',
  'open-mistral-7b', 'open-mixtral-8x7b', 'open-mixtral-8x22b',
  'codestral-latest', 'codestral-mamba',
  'pixtral-12b-2409', 'pixtral-large-latest'
]

// Meta Llama
const metaModels = [
  'llama-3.3-70b-instruct',
  'llama-3.2-90b-vision-instruct', 'llama-3.2-11b-vision-instruct',
  'llama-3.2-3b-instruct', 'llama-3.2-1b-instruct',
  'llama-3.1-405b-instruct', 'llama-3.1-70b-instruct', 'llama-3.1-8b-instruct',
  'llama-3-70b-instruct', 'llama-3-8b-instruct',
  'codellama-70b-instruct', 'codellama-34b-instruct', 'codellama-13b-instruct'
]

// xAI Grok
const xaiModels = [
  'grok-4.7',
  'grok-4.6',
  'grok-4.5',
  'grok-4.3',
  'grok-build-0.1',
  'grok-composer-2.5-fast',
  'grok-4.20-0309-reasoning',
  'grok-4.20-0309-non-reasoning',
  'grok-4.20-multi-agent-0309',
  'grok-4.20-multi-agent',
  'grok-4.20-multi-agent-latest',
  'grok-4.3-latest',
  'grok-latest',
  'grok-4.6-latest',
  'grok-4.5-latest',
  'grok-build-latest',
  'composer-2.5',
  'grok-4.20-reasoning',
  'grok-4.20-non-reasoning',
  'grok-imagine',
  'grok-imagine-image-quality',
  'grok-imagine-image',
  'grok-imagine-image-2.0',
  'grok-imagine-video',
  'grok-imagine-video-1.5'
]

// Cohere
const cohereModels = [
  'command-a-03-2025',
  'command-r', 'command-r-plus',
  'command-r-08-2024', 'command-r-plus-08-2024',
  'c4ai-aya-23-35b', 'c4ai-aya-23-8b',
  'command', 'command-light'
]

// Yi (01.AI)
const yiModels = [
  'yi-large', 'yi-large-turbo', 'yi-large-rag',
  'yi-medium', 'yi-medium-200k',
  'yi-spark', 'yi-vision',
  'yi-1.5-34b-chat', 'yi-1.5-9b-chat', 'yi-1.5-6b-chat'
]

// Moonshot/Kimi
const moonshotModels = [
  'moonshot-v1-8k', 'moonshot-v1-32k', 'moonshot-v1-128k',
  'kimi-latest',
  'kimi-for-coding',
  'kimi-k2'
]

// 字节跳动 豆包
const doubaoModels = [
  'doubao-pro-256k', 'doubao-pro-128k', 'doubao-pro-32k', 'doubao-pro-4k',
  'doubao-lite-128k', 'doubao-lite-32k', 'doubao-lite-4k',
  'doubao-vision-pro-32k', 'doubao-vision-lite-32k',
  'doubao-1.5-pro-256k', 'doubao-1.5-pro-32k', 'doubao-1.5-lite-32k',
  'doubao-1.5-pro-vision-32k', 'doubao-1.5-thinking-pro'
]

// MiniMax
const minimaxModels = [
  'MiniMax-M3',
  'MiniMax-M2.7',
  'MiniMax-M2.7-highspeed',
  'MiniMax-M2.5',
  'MiniMax-M2.5-highspeed',
  'MiniMax-M2.1',
  'MiniMax-M2.1-highspeed',
  'MiniMax-M2',
  'abab6.5-chat', 'abab6.5s-chat', 'abab6.5s-chat-pro',
  'abab6-chat',
  'abab5.5-chat', 'abab5.5s-chat'
]

// 百度 文心
const baiduModels = [
  'ernie-4.0-8k-latest', 'ernie-4.0-8k', 'ernie-4.0-turbo-8k',
  'ernie-3.5-8k', 'ernie-3.5-128k',
  'ernie-speed-8k', 'ernie-speed-128k', 'ernie-speed-pro-128k',
  'ernie-lite-8k', 'ernie-lite-pro-128k',
  'ernie-tiny-8k'
]

// 讯飞 星火
const sparkModels = [
  'spark-desk', 'spark-desk-v1.1', 'spark-desk-v2.1',
  'spark-desk-v3.1', 'spark-desk-v3.5', 'spark-desk-v4.0',
  'spark-lite', 'spark-pro', 'spark-max', 'spark-ultra'
]

// 腾讯 混元
const hunyuanModels = [
  'hunyuan-lite', 'hunyuan-standard', 'hunyuan-standard-256k',
  'hunyuan-pro', 'hunyuan-turbo', 'hunyuan-large',
  'hunyuan-vision', 'hunyuan-code'
]

// Perplexity
const perplexityModels = [
  'sonar', 'sonar-pro', 'sonar-reasoning',
  'llama-3-sonar-small-32k-online', 'llama-3-sonar-large-32k-online',
  'llama-3-sonar-small-32k-chat', 'llama-3-sonar-large-32k-chat'
]

// 所有模型（去重）
const allModelsList: string[] = Array.from(new Set([
  ...openaiModels,
  ...claudeModels,
  ...geminiModels,
  ...antigravityModels,
  ...zhipuModels,
  ...qwenModels,
  ...deepseekModels,
  ...mistralModels,
  ...metaModels,
  ...xaiModels,
  ...cohereModels,
  ...yiModels,
  ...moonshotModels,
  ...doubaoModels,
  ...minimaxModels,
  ...baiduModels,
  ...sparkModels,
  ...hunyuanModels,
  ...perplexityModels
]))

// 转换为下拉选项格式
export const allModels = allModelsList.map(m => ({ value: m, label: m }))

// =====================
// 预设映射
// =====================

const anthropicPresetMappings = [
  { label: 'Fable 5.1', from: 'claude-fable-5-1', to: 'claude-fable-5-1', color: 'bg-rose-100 text-rose-700 hover:bg-rose-200 dark:bg-rose-900/30 dark:text-rose-400' },
  { label: 'Fable 5', from: 'claude-fable-5', to: 'claude-fable-5', color: 'bg-rose-100 text-rose-700 hover:bg-rose-200 dark:bg-rose-900/30 dark:text-rose-400' },
  { label: 'Sonnet 5.5', from: 'claude-sonnet-5-5', to: 'claude-sonnet-5-5', color: 'bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-400' },
  { label: 'Sonnet 5', from: 'claude-sonnet-5', to: 'claude-sonnet-5', color: 'bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-400' },
  { label: 'Sonnet 4', from: 'claude-sonnet-4-20250514', to: 'claude-sonnet-4-20250514', color: 'bg-blue-100 text-blue-700 hover:bg-blue-200 dark:bg-blue-900/30 dark:text-blue-400' },
  { label: 'Sonnet 4.5', from: 'claude-sonnet-4-5-20250929', to: 'claude-sonnet-4-5-20250929', color: 'bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-400' },
  { label: 'Sonnet 4.6', from: 'claude-sonnet-4-6', to: 'claude-sonnet-4-6', color: 'bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-400' },
  { label: 'Opus 4.5', from: 'claude-opus-4-5-20251101', to: 'claude-opus-4-5-20251101', color: 'bg-purple-100 text-purple-700 hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-400' },
  { label: 'Opus 4.6', from: 'claude-opus-4-6', to: 'claude-opus-4-6', color: 'bg-purple-100 text-purple-700 hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-400' },
  { label: 'Opus 4.7', from: 'claude-opus-4-7', to: 'claude-opus-4-7', color: 'bg-purple-100 text-purple-700 hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-400' },
  { label: 'Opus 4.8', from: 'claude-opus-4-8', to: 'claude-opus-4-8', color: 'bg-purple-100 text-purple-700 hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-400' },
  { label: 'Opus 5.5', from: 'claude-opus-5-5', to: 'claude-opus-5-5', color: 'bg-purple-100 text-purple-700 hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-400' },
  { label: 'Opus 5', from: 'claude-opus-5', to: 'claude-opus-5', color: 'bg-purple-100 text-purple-700 hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-400' },
  { label: 'Haiku 3.5', from: 'claude-3-5-haiku-20241022', to: 'claude-3-5-haiku-20241022', color: 'bg-green-100 text-green-700 hover:bg-green-200 dark:bg-green-900/30 dark:text-green-400' },
  { label: 'Haiku 4.5', from: 'claude-haiku-4-5-20251001', to: 'claude-haiku-4-5-20251001', color: 'bg-emerald-100 text-emerald-700 hover:bg-emerald-200 dark:bg-emerald-900/30 dark:text-emerald-400' },
  { label: 'Haiku 5.5', from: 'claude-haiku-5-5', to: 'claude-haiku-5-5', color: 'bg-emerald-100 text-emerald-700 hover:bg-emerald-200 dark:bg-emerald-900/30 dark:text-emerald-400' },
  { label: 'Opus->Sonnet', from: 'claude-opus-4-6', to: 'claude-sonnet-4-5-20250929', color: 'bg-amber-100 text-amber-700 hover:bg-amber-200 dark:bg-amber-900/30 dark:text-amber-400' }
]

const openaiPresetMappings = [
  { label: 'GPT-4o', from: 'gpt-4o', to: 'gpt-4o', color: 'bg-green-100 text-green-700 hover:bg-green-200 dark:bg-green-900/30 dark:text-green-400' },
  { label: 'GPT-4o Mini', from: 'gpt-4o-mini', to: 'gpt-4o-mini', color: 'bg-blue-100 text-blue-700 hover:bg-blue-200 dark:bg-blue-900/30 dark:text-blue-400' },
  { label: 'GPT-4.1', from: 'gpt-4.1', to: 'gpt-4.1', color: 'bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-400' },
  { label: 'o1', from: 'o1', to: 'o1', color: 'bg-purple-100 text-purple-700 hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-400' },
  { label: 'o3', from: 'o3', to: 'o3', color: 'bg-emerald-100 text-emerald-700 hover:bg-emerald-200 dark:bg-emerald-900/30 dark:text-emerald-400' },
  { label: 'GPT-5.3 Codex Spark', from: 'gpt-5.3-codex-spark', to: 'gpt-5.3-codex-spark', color: 'bg-teal-100 text-teal-700 hover:bg-teal-200 dark:bg-teal-900/30 dark:text-teal-400' },
  { label: 'GPT-5.2', from: 'gpt-5.2', to: 'gpt-5.2', color: 'bg-red-100 text-red-700 hover:bg-red-200 dark:bg-red-900/30 dark:text-red-400' },
  { label: 'GPT-6', from: 'gpt-6', to: 'gpt-6', color: 'bg-cyan-100 text-cyan-700 hover:bg-cyan-200 dark:bg-cyan-900/30 dark:text-cyan-400' },
  { label: 'GPT-5.6', from: 'gpt-5.6', to: 'gpt-5.6', color: 'bg-amber-100 text-amber-700 hover:bg-amber-200 dark:bg-amber-900/30 dark:text-amber-400' },
  { label: 'GPT-6.1 Terra', from: 'gpt-6.1-terra', to: 'gpt-6.1-terra', color: 'bg-lime-100 text-lime-700 hover:bg-lime-200 dark:bg-lime-900/30 dark:text-lime-400' },
  { label: 'GPT-6.1 Sol', from: 'gpt-6.1-sol', to: 'gpt-6.1-sol', color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-400' },
  { label: 'GPT-6 Sol', from: 'gpt-6-sol', to: 'gpt-6-sol', color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-400' },
  { label: 'GPT-5.6 Sol', from: 'gpt-5.6-sol', to: 'gpt-5.6-sol', color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-400' },
  { label: 'GPT-5.6 Terra', from: 'gpt-5.6-terra', to: 'gpt-5.6-terra', color: 'bg-lime-100 text-lime-700 hover:bg-lime-200 dark:bg-lime-900/30 dark:text-lime-400' },
  { label: 'GPT-6 Luna', from: 'gpt-6-luna', to: 'gpt-6-luna', color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-400' },
  { label: 'GPT-5.6 Luna', from: 'gpt-5.6-luna', to: 'gpt-5.6-luna', color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-400' },
  { label: 'GPT-6 Astra', from: 'gpt-6-astra', to: 'gpt-6-astra', color: 'bg-fuchsia-100 text-fuchsia-700 hover:bg-fuchsia-200 dark:bg-fuchsia-900/30 dark:text-fuchsia-400' },
  { label: 'GPT-5.5', from: 'gpt-5.5', to: 'gpt-5.5', color: 'bg-amber-100 text-amber-700 hover:bg-amber-200 dark:bg-amber-900/30 dark:text-amber-400' },
  { label: 'GPT-5.4', from: 'gpt-5.4', to: 'gpt-5.4', color: 'bg-rose-100 text-rose-700 hover:bg-rose-200 dark:bg-rose-900/30 dark:text-rose-400' },
  { label: 'Haiku→5.4', from: 'claude-haiku-4-5-20251001', to: 'gpt-5.4', color: 'bg-emerald-100 text-emerald-700 hover:bg-emerald-200 dark:bg-emerald-900/30 dark:text-emerald-400' },
  { label: 'Opus→5.4', from: 'claude-opus-4-6', to: 'gpt-5.4', color: 'bg-purple-100 text-purple-700 hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-400' },
  { label: 'Sonnet→5.4', from: 'claude-sonnet-4-6', to: 'gpt-5.4', color: 'bg-blue-100 text-blue-700 hover:bg-blue-200 dark:bg-blue-900/30 dark:text-blue-400' }
]

const geminiPresetMappings = [
  { label: 'Flash 2.0', from: 'gemini-2.0-flash', to: 'gemini-2.0-flash', color: 'bg-blue-100 text-blue-700 hover:bg-blue-200 dark:bg-blue-900/30 dark:text-blue-400' },
  { label: '2.5 Flash', from: 'gemini-2.5-flash', to: 'gemini-2.5-flash', color: 'bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-400' },
  { label: '2.5 Image', from: 'gemini-2.5-flash-image', to: 'gemini-2.5-flash-image', color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-400' },
  { label: '2.5 Pro', from: 'gemini-2.5-pro', to: 'gemini-2.5-pro', color: 'bg-purple-100 text-purple-700 hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-400' },
  { label: '3.5 Flash', from: 'gemini-3.5-flash', to: 'gemini-3.5-flash', color: 'bg-violet-100 text-violet-700 hover:bg-violet-200 dark:bg-violet-900/30 dark:text-violet-400' },
  { label: '3.1 Image', from: 'gemini-3.1-flash-image', to: 'gemini-3.1-flash-image', color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-400' }
]

const grokPresetMappings = [
  { label: 'Grok 4.7', from: 'grok-4.7', to: 'grok-4.7', color: 'bg-slate-100 text-slate-700 hover:bg-slate-200 dark:bg-slate-800/50 dark:text-slate-300' },
  { label: 'Grok 4.6', from: 'grok-4.6', to: 'grok-4.6', color: 'bg-slate-100 text-slate-700 hover:bg-slate-200 dark:bg-slate-800/50 dark:text-slate-300' },
  { label: 'Grok 4.5', from: 'grok-4.5', to: 'grok-4.5', color: 'bg-slate-100 text-slate-700 hover:bg-slate-200 dark:bg-slate-800/50 dark:text-slate-300' },
  { label: 'Grok 4.3', from: 'grok-4.3', to: 'grok-4.3', color: 'bg-slate-100 text-slate-700 hover:bg-slate-200 dark:bg-slate-800/50 dark:text-slate-300' },
  { label: 'Grok Latest', from: 'grok-latest', to: 'grok-4.5', color: 'bg-emerald-100 text-emerald-700 hover:bg-emerald-200 dark:bg-emerald-900/30 dark:text-emerald-400' },
  { label: '4.5 Latest', from: 'grok-4.5-latest', to: 'grok-4.5', color: 'bg-lime-100 text-lime-700 hover:bg-lime-200 dark:bg-lime-900/30 dark:text-lime-400' },
  { label: 'Build 0.1', from: 'grok-build', to: 'grok-build-0.1', color: 'bg-cyan-100 text-cyan-700 hover:bg-cyan-200 dark:bg-cyan-900/30 dark:text-cyan-400' },
  { label: 'Build Latest', from: 'grok-build-latest', to: 'grok-4.5', color: 'bg-teal-100 text-teal-700 hover:bg-teal-200 dark:bg-teal-900/30 dark:text-teal-400' },
  { label: 'Composer 2.5', from: 'grok-composer', to: 'grok-composer-2.5-fast', color: 'bg-teal-100 text-teal-700 hover:bg-teal-200 dark:bg-teal-900/30 dark:text-teal-400' },
  { label: 'Composer legacy', from: 'composer-2.5', to: 'grok-composer-2.5-fast', color: 'bg-teal-100 text-teal-700 hover:bg-teal-200 dark:bg-teal-900/30 dark:text-teal-400' },
  { label: '4.20 Reasoning', from: 'grok-4.20-reasoning', to: 'grok-4.20-0309-reasoning', color: 'bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-400' },
  { label: '4.20 Non Reasoning', from: 'grok-4.20-non-reasoning', to: 'grok-4.20-0309-non-reasoning', color: 'bg-violet-100 text-violet-700 hover:bg-violet-200 dark:bg-violet-900/30 dark:text-violet-400' },
  { label: 'Imagine Image', from: 'grok-imagine', to: 'grok-imagine-image-quality', color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-400' },
  { label: 'Imagine Edit', from: 'grok-imagine-edit', to: 'grok-imagine-image-quality', color: 'bg-rose-100 text-rose-700 hover:bg-rose-200 dark:bg-rose-900/30 dark:text-rose-400' },
  { label: 'Imagine Video', from: 'grok-imagine-video-1.5', to: 'grok-imagine-video-1.5', color: 'bg-amber-100 text-amber-700 hover:bg-amber-200 dark:bg-amber-900/30 dark:text-amber-400' }
]

// Antigravity 预设映射（支持通配符）
const antigravityPresetMappings = [
  // Claude 5.5 / Gemini 4 Argon / 3.8 Flash 推荐映射
  { label: '4 Argon→High', from: 'gemini-4-argon', to: 'gemini-4-argon-high', color: 'bg-amber-100 text-amber-700 hover:bg-amber-200 dark:bg-amber-900/30 dark:text-amber-400' },
  { label: '4 Argon High', from: 'gemini-4-argon-high', to: 'gemini-4-argon-high', color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-400' },
  { label: '4 Argon Medium', from: 'gemini-4-argon-medium', to: 'gemini-4-argon-medium', color: 'bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-400' },
  { label: '4 Argon Low', from: 'gemini-4-argon-low', to: 'gemini-4-argon-low', color: 'bg-lime-100 text-lime-700 hover:bg-lime-200 dark:bg-lime-900/30 dark:text-lime-400' },
  { label: '3.8 Flash', from: 'gemini-3.8-flash', to: 'gemini-3.8-flash', color: 'bg-emerald-100 text-emerald-700 hover:bg-emerald-200 dark:bg-emerald-900/30 dark:text-emerald-400' },
  { label: '3.8 Flash High', from: 'gemini-3.8-flash-high', to: 'gemini-3.8-flash-high', color: 'bg-teal-100 text-teal-700 hover:bg-teal-200 dark:bg-teal-900/30 dark:text-teal-400' },
  { label: 'Opus 5.5→High', from: 'claude-opus-5-5', to: 'claude-opus-5-5-high', color: 'bg-pink-100 text-pink-700 hover:bg-pink-200 dark:bg-pink-900/30 dark:text-pink-400' },
  { label: 'Opus 5.5 High', from: 'claude-opus-5-5-high', to: 'claude-opus-5-5-high', color: 'bg-pink-100 text-pink-700 hover:bg-pink-200 dark:bg-pink-900/30 dark:text-pink-400' },
  { label: 'Opus 5.5 Medium', from: 'claude-opus-5-5-medium', to: 'claude-opus-5-5-medium', color: 'bg-purple-100 text-purple-700 hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-400' },
  { label: 'Sonnet 5.5→High', from: 'claude-sonnet-5-5', to: 'claude-sonnet-5-5-high', color: 'bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-400' },
  { label: 'Sonnet 5.5 High', from: 'claude-sonnet-5-5-high', to: 'claude-sonnet-5-5-high', color: 'bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-400' },
  { label: 'Sonnet 5.5 Medium', from: 'claude-sonnet-5-5-medium', to: 'claude-sonnet-5-5-medium', color: 'bg-cyan-100 text-cyan-700 hover:bg-cyan-200 dark:bg-cyan-900/30 dark:text-cyan-400' },
  // Claude 通配符映射
  { label: 'Claude→Sonnet 5.5', from: 'claude-*', to: 'claude-sonnet-5-5-high', color: 'bg-blue-100 text-blue-700 hover:bg-blue-200 dark:bg-blue-900/30 dark:text-blue-400' },
  { label: 'Opus→Opus 5.5', from: 'claude-opus-*', to: 'claude-opus-5-5-high', color: 'bg-purple-100 text-purple-700 hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-400' },
  { label: 'Claude→Sonnet', from: 'claude-*', to: 'claude-sonnet-4-5', color: 'bg-blue-100 text-blue-700 hover:bg-blue-200 dark:bg-blue-900/30 dark:text-blue-400' },
  { label: 'Fable 5.1', from: 'claude-fable-5-1', to: 'claude-fable-5-1', color: 'bg-rose-100 text-rose-700 hover:bg-rose-200 dark:bg-rose-900/30 dark:text-rose-400' },
  { label: 'Fable 5', from: 'claude-fable-5', to: 'claude-fable-5', color: 'bg-rose-100 text-rose-700 hover:bg-rose-200 dark:bg-rose-900/30 dark:text-rose-400' },
  { label: 'Sonnet→Sonnet', from: 'claude-sonnet-*', to: 'claude-sonnet-4-5', color: 'bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-400' },
  { label: 'Opus→Opus', from: 'claude-opus-*', to: 'claude-opus-4-6-thinking', color: 'bg-purple-100 text-purple-700 hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-400' },
  { label: 'Haiku→Sonnet', from: 'claude-haiku-*', to: 'claude-sonnet-4-5', color: 'bg-emerald-100 text-emerald-700 hover:bg-emerald-200 dark:bg-emerald-900/30 dark:text-emerald-400' },
  { label: 'Sonnet4→4.6', from: 'claude-sonnet-4-20250514', to: 'claude-sonnet-4-6', color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-400' },
  { label: 'Sonnet4.5→4.6', from: 'claude-sonnet-4-5-20250929', to: 'claude-sonnet-4-6', color: 'bg-cyan-100 text-cyan-700 hover:bg-cyan-200 dark:bg-cyan-900/30 dark:text-cyan-400' },
  { label: 'Sonnet3.5→4.6', from: 'claude-3-5-sonnet-20241022', to: 'claude-sonnet-4-6', color: 'bg-teal-100 text-teal-700 hover:bg-teal-200 dark:bg-teal-900/30 dark:text-teal-400' },
  { label: 'Opus4.5→4.6', from: 'claude-opus-4-5-20251101', to: 'claude-opus-4-6-thinking', color: 'bg-violet-100 text-violet-700 hover:bg-violet-200 dark:bg-violet-900/30 dark:text-violet-400' },
  // Gemini 3→3.1 / 4 Argon 映射
  { label: '3.1-Pro→4-Argon-High', from: 'gemini-3.1-pro*', to: 'gemini-4-argon-high', color: 'bg-amber-100 text-amber-700 hover:bg-amber-200 dark:bg-amber-900/30 dark:text-amber-400' },
  { label: '3-Pro-Preview→Pro-Agent', from: 'gemini-3-pro-preview', to: 'gemini-pro-agent', color: 'bg-amber-100 text-amber-700 hover:bg-amber-200 dark:bg-amber-900/30 dark:text-amber-400' },
  { label: '3-Pro-High→Pro-Agent', from: 'gemini-3-pro-high', to: 'gemini-pro-agent', color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-400' },
  { label: '3-Pro-Low→3.1-Pro-Low', from: 'gemini-3-pro-low', to: 'gemini-3.1-pro-low', color: 'bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-400' },
  { label: '3.1-Pro→Pro-Agent', from: 'gemini-3.1-pro', to: 'gemini-pro-agent', color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-400' },
  { label: '3.1-Pro-High→Pro-Agent', from: 'gemini-3.1-pro-high', to: 'gemini-pro-agent', color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-400' },
  { label: '3.1-Pro-High透传', from: 'gemini-3.1-pro-high', to: 'gemini-3.1-pro-high', color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-400' },
  { label: '3.1-Pro-Low透传', from: 'gemini-3.1-pro-low', to: 'gemini-3.1-pro-low', color: 'bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-400' },
  { label: '3.1-Pro-Preview→Pro-Agent', from: 'gemini-3.1-pro-preview', to: 'gemini-pro-agent', color: 'bg-amber-100 text-amber-700 hover:bg-amber-200 dark:bg-amber-900/30 dark:text-amber-400' },
  // Gemini 通配符映射
  { label: 'Gemini 3→3.8 Flash', from: 'gemini-3*', to: 'gemini-3.8-flash-high', color: 'bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-400' },
  { label: 'Gemini 3→Flash', from: 'gemini-3*', to: 'gemini-3-flash', color: 'bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-400' },
  { label: 'Gemini 2.5→Flash', from: 'gemini-2.5*', to: 'gemini-2.5-flash', color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-400' },
  { label: '2.5-Flash-Image透传', from: 'gemini-2.5-flash-image', to: 'gemini-2.5-flash-image', color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-400' },
  { label: '3.1-Flash-Image透传', from: 'gemini-3.1-flash-image', to: 'gemini-3.1-flash-image', color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-400' },
  { label: '3-Pro-Image→3.1', from: 'gemini-3-pro-image', to: 'gemini-3.1-flash-image', color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-400' },
  { label: '3-Flash透传', from: 'gemini-3-flash', to: 'gemini-3-flash', color: 'bg-lime-100 text-lime-700 hover:bg-lime-200 dark:bg-lime-900/30 dark:text-lime-400' },
  { label: '2.5-Flash-Lite透传', from: 'gemini-2.5-flash-lite', to: 'gemini-2.5-flash-lite', color: 'bg-green-100 text-green-700 hover:bg-green-200 dark:bg-green-900/30 dark:text-green-400' },
  // 精确映射
  { label: 'Sonnet 4.6', from: 'claude-sonnet-4-6', to: 'claude-sonnet-4-6', color: 'bg-cyan-100 text-cyan-700 hover:bg-cyan-200 dark:bg-cyan-900/30 dark:text-cyan-400' },
  { label: 'Sonnet 4.5', from: 'claude-sonnet-4-5', to: 'claude-sonnet-4-5', color: 'bg-cyan-100 text-cyan-700 hover:bg-cyan-200 dark:bg-cyan-900/30 dark:text-cyan-400' },
  { label: 'Opus 4.6', from: 'claude-opus-4-6', to: 'claude-opus-4-6-thinking', color: 'bg-pink-100 text-pink-700 hover:bg-pink-200 dark:bg-pink-900/30 dark:text-pink-400' },
  { label: 'Opus 4.6-thinking', from: 'claude-opus-4-6-thinking', to: 'claude-opus-4-6-thinking', color: 'bg-pink-100 text-pink-700 hover:bg-pink-200 dark:bg-pink-900/30 dark:text-pink-400' },
  { label: 'Opus 4.7', from: 'claude-opus-4-7', to: 'claude-opus-4-7', color: 'bg-pink-100 text-pink-700 hover:bg-pink-200 dark:bg-pink-900/30 dark:text-pink-400' },
  { label: 'Opus 4.8', from: 'claude-opus-4-8', to: 'claude-opus-4-8', color: 'bg-pink-100 text-pink-700 hover:bg-pink-200 dark:bg-pink-900/30 dark:text-pink-400' }
]

const kiroPresetMappings = [
  { label: 'GPT-5.6 Sol', from: 'gpt-5.6-sol', to: 'gpt-5.6-sol', color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-300' },
  { label: 'GPT-5.6 Terra', from: 'gpt-5.6-terra', to: 'gpt-5.6-terra', color: 'bg-lime-100 text-lime-700 hover:bg-lime-200 dark:bg-lime-900/30 dark:text-lime-300' },
  { label: 'GPT-5.6 Luna', from: 'gpt-5.6-luna', to: 'gpt-5.6-luna', color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-300' },
  { label: 'Kimi K3', from: 'kimi-k3', to: 'kimi-k3', color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-300' },
  { label: 'Codex Auto Review→Luna', from: 'codex-auto-review', to: 'gpt-5.6-luna', color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-300' },
  { label: 'Opus 4.8', from: 'claude-opus-4-8', to: 'claude-opus-4.8', color: 'bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-300' },
  { label: 'Opus 4.8 Thinking', from: 'claude-opus-4-8-thinking', to: 'claude-opus-4.8', color: 'bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-300' },
  { label: 'Opus 4.7', from: 'claude-opus-4-7', to: 'claude-opus-4.7', color: 'bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-300' },
  { label: 'Opus 4.7 Thinking', from: 'claude-opus-4-7-thinking', to: 'claude-opus-4.7', color: 'bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-300' },
  { label: 'Opus 4.6', from: 'claude-opus-4-6', to: 'claude-opus-4.6', color: 'bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-300' },
  { label: 'Opus 4.6 Thinking', from: 'claude-opus-4-6-thinking', to: 'claude-opus-4.6', color: 'bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-300' },
  { label: 'Opus 5', from: 'claude-opus-5', to: 'claude-opus-5', color: 'bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-300' },
  { label: 'Opus 5 Thinking', from: 'claude-opus-5-thinking', to: 'claude-opus-5', color: 'bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-300' },
  { label: 'Opus 5.5', from: 'claude-opus-5-5', to: 'claude-opus-5.5', color: 'bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-300' },
  { label: 'Opus 5.5 Thinking', from: 'claude-opus-5-5-thinking', to: 'claude-opus-5.5', color: 'bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-300' },
  { label: 'Sonnet 5', from: 'claude-sonnet-5', to: 'claude-sonnet-5', color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-300' },
  { label: 'Sonnet 5 Thinking', from: 'claude-sonnet-5-thinking', to: 'claude-sonnet-5', color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-300' },
  { label: 'Sonnet 5.5', from: 'claude-sonnet-5-5', to: 'claude-sonnet-5.5', color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-300' },
  { label: 'Sonnet 5.5 Thinking', from: 'claude-sonnet-5-5-thinking', to: 'claude-sonnet-5.5', color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-300' },
  { label: 'Sonnet 4.6', from: 'claude-sonnet-4-6', to: 'claude-sonnet-4.6', color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-300' },
  { label: 'Sonnet 4.6 Thinking', from: 'claude-sonnet-4-6-thinking', to: 'claude-sonnet-4.6', color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-300' },
  { label: 'Opus 4.5', from: 'claude-opus-4-5-20251101', to: 'claude-opus-4.5', color: 'bg-pink-100 text-pink-700 hover:bg-pink-200 dark:bg-pink-900/30 dark:text-pink-300' },
  { label: 'Opus 4.5 Thinking', from: 'claude-opus-4-5-20251101-thinking', to: 'claude-opus-4.5', color: 'bg-pink-100 text-pink-700 hover:bg-pink-200 dark:bg-pink-900/30 dark:text-pink-300' },
  { label: 'Sonnet 4.5', from: 'claude-sonnet-4-5-20250929', to: 'claude-sonnet-4.5', color: 'bg-cyan-100 text-cyan-700 hover:bg-cyan-200 dark:bg-cyan-900/30 dark:text-cyan-300' },
  { label: 'Sonnet 4.5 Thinking', from: 'claude-sonnet-4-5-20250929-thinking', to: 'claude-sonnet-4.5', color: 'bg-cyan-100 text-cyan-700 hover:bg-cyan-200 dark:bg-cyan-900/30 dark:text-cyan-300' },
  { label: 'Haiku 4.5', from: 'claude-haiku-4-5-20251001', to: 'claude-haiku-4.5', color: 'bg-green-100 text-green-700 hover:bg-green-200 dark:bg-green-900/30 dark:text-green-300' },
  { label: 'Haiku 4.5 Thinking', from: 'claude-haiku-4-5-20251001-thinking', to: 'claude-haiku-4.5', color: 'bg-green-100 text-green-700 hover:bg-green-200 dark:bg-green-900/30 dark:text-green-300' },
  { label: 'Haiku 5.5', from: 'claude-haiku-5-5', to: 'claude-haiku-5.5', color: 'bg-green-100 text-green-700 hover:bg-green-200 dark:bg-green-900/30 dark:text-green-300' },
  { label: 'Haiku 5.5 Thinking', from: 'claude-haiku-5-5-thinking', to: 'claude-haiku-5.5', color: 'bg-green-100 text-green-700 hover:bg-green-200 dark:bg-green-900/30 dark:text-green-300' }
]

// Bedrock 预设映射（与后端 DefaultBedrockModelMapping 保持一致）
const bedrockPresetMappings = [
  { label: 'Fable 5.1', from: 'claude-fable-5-1', to: 'anthropic.claude-fable-5-1', color: 'bg-rose-100 text-rose-700 hover:bg-rose-200 dark:bg-rose-900/30 dark:text-rose-400' },
  { label: 'Fable 5', from: 'claude-fable-5', to: 'anthropic.claude-fable-5', color: 'bg-rose-100 text-rose-700 hover:bg-rose-200 dark:bg-rose-900/30 dark:text-rose-400' },
  { label: 'Opus 4.6', from: 'claude-opus-4-6', to: 'us.anthropic.claude-opus-4-6-v1', color: 'bg-pink-100 text-pink-700 hover:bg-pink-200 dark:bg-pink-900/30 dark:text-pink-400' },
  { label: 'Opus 4.7', from: 'claude-opus-4-7', to: 'us.anthropic.claude-opus-4-7-v1', color: 'bg-pink-100 text-pink-700 hover:bg-pink-200 dark:bg-pink-900/30 dark:text-pink-400' },
  { label: 'Opus 4.8', from: 'claude-opus-4-8', to: 'us.anthropic.claude-opus-4-8-v1', color: 'bg-pink-100 text-pink-700 hover:bg-pink-200 dark:bg-pink-900/30 dark:text-pink-400' },
  { label: 'Opus 5', from: 'claude-opus-5', to: 'us.anthropic.claude-opus-5-v1', color: 'bg-pink-100 text-pink-700 hover:bg-pink-200 dark:bg-pink-900/30 dark:text-pink-400' },
  { label: 'Sonnet 5.5', from: 'claude-sonnet-5-5', to: 'global.anthropic.claude-sonnet-5-5', color: 'bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-400' },
  { label: 'Sonnet 5', from: 'claude-sonnet-5', to: 'us.anthropic.claude-sonnet-5-v1', color: 'bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-400' },
  { label: 'Sonnet 4.6', from: 'claude-sonnet-4-6', to: 'us.anthropic.claude-sonnet-4-6', color: 'bg-cyan-100 text-cyan-700 hover:bg-cyan-200 dark:bg-cyan-900/30 dark:text-cyan-400' },
  { label: 'Opus 4.5', from: 'claude-opus-4-5-thinking', to: 'us.anthropic.claude-opus-4-5-20251101-v1:0', color: 'bg-pink-100 text-pink-700 hover:bg-pink-200 dark:bg-pink-900/30 dark:text-pink-400' },
  { label: 'Sonnet 4.5', from: 'claude-sonnet-4-5', to: 'us.anthropic.claude-sonnet-4-5-20250929-v1:0', color: 'bg-cyan-100 text-cyan-700 hover:bg-cyan-200 dark:bg-cyan-900/30 dark:text-cyan-400' },
  { label: 'Haiku 4.5', from: 'claude-haiku-4-5', to: 'us.anthropic.claude-haiku-4-5-20251001-v1:0', color: 'bg-green-100 text-green-700 hover:bg-green-200 dark:bg-green-900/30 dark:text-green-400' },
]

const kiroDefaultMappings = kiroPresetMappings.map(({ from, to }) => ({ from, to }))

// Antigravity 默认映射（从后端 API 获取，与 constants.go 保持一致）
// 使用 fetchAntigravityDefaultMappings() 异步获取
import { getAntigravityDefaultModelMapping } from '@/api/admin/accounts'

let _antigravityDefaultMappingsCache: { from: string; to: string }[] | null = null
let _kiroDefaultMappingsCache: { from: string; to: string }[] | null = null

export async function fetchAntigravityDefaultMappings(): Promise<{ from: string; to: string }[]> {
  if (_antigravityDefaultMappingsCache !== null) {
    return _antigravityDefaultMappingsCache.map(({ from, to }) => ({ from, to }))
  }
  try {
    const mapping = await getAntigravityDefaultModelMapping()
    _antigravityDefaultMappingsCache = Object.entries(mapping).map(([from, to]) => ({ from, to }))
  } catch (e) {
    console.warn('[fetchAntigravityDefaultMappings] API failed, using empty fallback', e)
    return []
  }
  return _antigravityDefaultMappingsCache.map(({ from, to }) => ({ from, to }))
}

export async function fetchKiroDefaultMappings(): Promise<{ from: string; to: string }[]> {
  if (_kiroDefaultMappingsCache !== null) {
    return _kiroDefaultMappingsCache.map(({ from, to }) => ({ from, to }))
  }
  _kiroDefaultMappingsCache = kiroDefaultMappings.map(({ from, to }) => ({ from, to }))
  return _kiroDefaultMappingsCache.map(({ from, to }) => ({ from, to }))
}

// =====================
// 常用错误码
// =====================

export const commonErrorCodes = [
  { value: 401, label: 'Unauthorized' },
  { value: 403, label: 'Forbidden' },
  { value: 429, label: 'Rate Limit' },
  { value: 500, label: 'Server Error' },
  { value: 502, label: 'Bad Gateway' },
  { value: 503, label: 'Unavailable' },
  { value: 529, label: 'Overloaded' }
]

// =====================
// 辅助函数
// =====================

// 按平台获取模型
export function getModelsByPlatform(platform: string): string[] {
  switch (platform) {
    case 'openai': return openaiModels
    case 'anthropic':
    case 'claude': return claudeModels
    case 'gemini': return geminiModels
    case 'antigravity': return antigravityModels
    case 'kiro': return kiroModels
    case 'leonardo': return leonardoModels
    case 'fish': return fishModels
    case 'zhipu': return zhipuModels
    case 'qwen': return qwenModels
    case 'deepseek': return deepseekModels
    case 'mistral': return mistralModels
    case 'meta': return metaModels
    case 'xai':
    case 'grok': return xaiModels
    case 'cohere': return cohereModels
    case 'yi': return yiModels
    case 'moonshot':
    case 'kimi': return moonshotModels
    case 'opencode_go': return [
      'grok-4.7', 'grok-4.6', 'gpt-5.6-luna',
      'glm-5.3-flash', 'glm-5.3', 'glm-5.2', 'glm-5.1',
      'kimi-k3', 'kimi-k2.7-code', 'kimi-k2.6',
      'longcat-2.0',
      'deepseek-v4-pro', 'deepseek-v4-flash', 'deepseek-v4-flash-vision-exp',
      'mimo-v2.5', 'mimo-v2.5-pro',
      'minimax-m3', 'minimax-m2.7', 'minimax-m2.5',
      'muse-spark-1.3-contributor', 'muse-spark-1.2-contributor',
      'qwen3.8-max', 'qwen3.8-flash', 'qwen3.7-max', 'qwen3.7-plus', 'qwen3.6-plus',
      'hy4-preview', 'hy3', 'omen-alpha'
    ]
    case 'typesafe': return ['jev-latest']
    case 'doubao': return doubaoModels
    case 'minimax': return minimaxModels
    case 'baidu': return baiduModels
    case 'spark': return sparkModels
    case 'hunyuan': return hunyuanModels
    case 'perplexity': return perplexityModels
    default:
      // 平台清单中没有内置模型列表的多协议供应商（多为多模型聚合平台，模型多且常变）
      // 默认不预填白名单：预填 Claude 模型会让新账号只接受这些模型。
      return getProviderProfile(platform) ? [] : claudeModels
  }
}

// 按平台获取预设映射
export function getPresetMappingsByPlatform(platform: string) {
  if (platform === 'openai') return openaiPresetMappings
  if (platform === 'gemini') return geminiPresetMappings
  if (platform === 'grok' || platform === 'xai') return grokPresetMappings
  if (platform === 'antigravity') return antigravityPresetMappings
  if (platform === 'kiro') return kiroPresetMappings
  if (platform === 'leonardo') return leonardoPresetMappings
  if (platform === 'fish') return fishPresetMappings
  if (platform === 'bedrock') return bedrockPresetMappings
  return anthropicPresetMappings
}

// BPS OAuth 账号只服务 BPS 模型：快速添加只给默认 BPS 模型（from = to），颜色沿用 OpenAI 预设。
export function getBPSPresetMappings() {
  return DEFAULT_EXCEL_BPS_MODELS.map(model =>
    openaiPresetMappings.find(preset => preset.from === model && preset.to === model) ?? {
      label: model,
      from: model,
      to: model,
      color: 'bg-gray-100 text-gray-700 hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-300'
    }
  )
}

// =====================
// 构建模型映射对象（用于 API）
// =====================

// isValidWildcardPattern 校验通配符格式：* 只能放在末尾
// 导出供表单组件使用实时校验
export function isValidWildcardPattern(pattern: string): boolean {
  const starIndex = pattern.indexOf('*')
  if (starIndex === -1) return true // 无通配符，有效
  // * 必须在末尾，且只能有一个
  return starIndex === pattern.length - 1 && pattern.lastIndexOf('*') === starIndex
}

export type ModelRestrictionMode = 'whitelist' | 'mapping' | 'combined'

export interface ModelMappingEntry {
  from: string
  to: string
}

export interface ModelMappingBuildOptions {
  // 直连 Kiro：白名单只在后端 MapModel 能折出同一个上游 ID 时存成恒等映射，
  // 折不出来的同步名字存成 对外名 → 上游 ID。
  kiroDirect?: boolean
  // 同步得到的对外名 → 上游 modelId。未知 Claude 名只认这份，不再猜点号位置。
  kiroUpstreamIDs?: Record<string, string> | null
}

// 与后端 kiro.MapModel 的明确条目保持一致。
const kiroKnownUpstreamIDs: Record<string, string> = {
  'gpt-5.6-sol': 'gpt-5.6-sol',
  'gpt-5.6-terra': 'gpt-5.6-terra',
  'gpt-5.6-luna': 'gpt-5.6-luna',
  'claude-opus-4-8': 'claude-opus-4.8',
  'claude-opus-4-8-thinking': 'claude-opus-4.8',
  'claude-opus-4.8': 'claude-opus-4.8',
  'claude-opus-4-7': 'claude-opus-4.7',
  'claude-opus-4-7-thinking': 'claude-opus-4.7',
  'claude-opus-4.7': 'claude-opus-4.7',
  'claude-opus-4-6': 'claude-opus-4.6',
  'claude-opus-4-6-thinking': 'claude-opus-4.6',
  'claude-opus-4.6': 'claude-opus-4.6',
  'claude-opus-5': 'claude-opus-5',
  'claude-opus-5-thinking': 'claude-opus-5',
  'claude-opus-5-5': 'claude-opus-5.5',
  'claude-opus-5-5-thinking': 'claude-opus-5.5',
  'claude-opus-5.5': 'claude-opus-5.5',
  'claude-opus-5.5-thinking': 'claude-opus-5.5',
  'claude-sonnet-5': 'claude-sonnet-5',
  'claude-sonnet-5-thinking': 'claude-sonnet-5',
  'claude-sonnet-5-5': 'claude-sonnet-5.5',
  'claude-sonnet-5-5-thinking': 'claude-sonnet-5.5',
  'claude-sonnet-5.5': 'claude-sonnet-5.5',
  'claude-sonnet-5.5-thinking': 'claude-sonnet-5.5',
  'claude-sonnet-4-6': 'claude-sonnet-4.6',
  'claude-sonnet-4-6-thinking': 'claude-sonnet-4.6',
  'claude-sonnet-4.6': 'claude-sonnet-4.6',
  'claude-opus-4-5-20251101': 'claude-opus-4.5',
  'claude-opus-4-5-20251101-thinking': 'claude-opus-4.5',
  'claude-opus-4.5': 'claude-opus-4.5',
  'claude-sonnet-4-5-20250929': 'claude-sonnet-4.5',
  'claude-sonnet-4-5-20250929-thinking': 'claude-sonnet-4.5',
  'claude-sonnet-4.5': 'claude-sonnet-4.5',
  'claude-haiku-4-5-20251001': 'claude-haiku-4.5',
  'claude-haiku-4-5-20251001-thinking': 'claude-haiku-4.5',
  'claude-haiku-4.5': 'claude-haiku-4.5',
  'claude-sonnet-4-thinking': 'claude-sonnet-4'
}

// 与后端 claudeVersionNormalizePattern 相同：不带日期的 claude-{family}-{major}-{minor}。
const kiroFoldableClaudePattern = /^(claude-(?:sonnet|haiku|opus))-(\d+)-(\d{1,2})(?:-thinking)?$/

// kiroForwardedModelID 模拟后端 resolveKiroUpstreamModel：账号映射目标为 model 时，
// 转发给上游的实际 modelId。先查固定表，再把 4.5 及以上的短横线版本折成点号，否则原样。
function kiroForwardedModelID(model: string): string {
  const trimmed = model.trim()
  const id = trimmed.toLowerCase()
  const known = kiroKnownUpstreamIDs[id]
  if (known) return known

  const base = id.endsWith('-thinking') ? id.slice(0, -'-thinking'.length) : id
  const matches = kiroFoldableClaudePattern.exec(base)
  if (matches) {
    const major = Number(matches[2])
    const minor = Number(matches[3])
    if (major > 4 || (major === 4 && minor >= 5)) {
      return `${matches[1]}-${matches[2]}.${matches[3]}`
    }
  }
  return trimmed
}

function kiroSyncedUpstreamID(model: string, upstreamIDs?: Record<string, string> | null): string {
  const trimmed = model.trim()
  const synced = upstreamIDs?.[trimmed] ?? upstreamIDs?.[trimmed.toLowerCase()]
  return synced?.trim() ?? ''
}

// 只有存成恒等后后端转发的上游 ID 正好等于 to，才算白名单。
// claude-opus-4-5 → claude-opus-4.5 归白名单；claude-3-7-sonnet → claude-3.7-sonnet 仍是映射。
function isKiroDirectWhitelistPair(from: string, to: string): boolean {
  return from === to || kiroForwardedModelID(from) === to
}

// kiroUpstreamModelID 先认默认表和日期别名，再用同步带回的上游 ID。
// 两边都没有的名字原样保存。
export function kiroUpstreamModelID(
  model: string,
  upstreamIDs?: Record<string, string> | null
): string {
  const trimmed = model.trim()
  const known = kiroKnownUpstreamIDs[trimmed.toLowerCase()]
  if (known) return known
  return kiroSyncedUpstreamID(trimmed, upstreamIDs) || trimmed
}

export function kiroUpstreamIDsFromMetadata(
  metadata?: Record<string, { id?: string } | undefined> | null
): Record<string, string> {
  const ids: Record<string, string> = {}
  if (!metadata) return ids
  for (const [name, entry] of Object.entries(metadata)) {
    const from = name.trim()
    const upstreamID = entry?.id?.trim()
    if (!from || !upstreamID) continue
    ids[from] = upstreamID
  }
  return ids
}

// appendKiroSyncedMappings 只追加还没有的 from。已知表优先，否则用同步带回的上游 ID。
export function appendKiroSyncedMappings(
  rows: ModelMappingEntry[],
  publicNames: string[],
  upstreamIDs?: Record<string, string> | null
): { rows: ModelMappingEntry[]; added: number } {
  const next = rows.map(row => ({ from: row.from, to: row.to }))
  const existing = new Set(next.map(row => row.from.trim()).filter(Boolean))
  let added = 0
  for (const name of publicNames) {
    const from = name.trim()
    if (!from || existing.has(from)) continue
    existing.add(from)
    next.push({ from, to: kiroUpstreamModelID(from, upstreamIDs) })
    added += 1
  }
  return { rows: next, added }
}

export function splitModelMappingObject(
  modelMapping?: Record<string, unknown> | null,
  options?: ModelMappingBuildOptions
): { allowedModels: string[]; modelMappings: ModelMappingEntry[] } {
  const allowedModels: string[] = []
  const modelMappings: ModelMappingEntry[] = []

  if (!modelMapping || typeof modelMapping !== 'object') {
    return { allowedModels, modelMappings }
  }

  for (const [rawFrom, rawTo] of Object.entries(modelMapping)) {
    if (typeof rawTo !== 'string') continue
    const from = rawFrom.trim()
    const to = rawTo.trim()
    if (!from || !to) continue

    const isWhitelist = options?.kiroDirect
      ? isKiroDirectWhitelistPair(from, to)
      : from === to
    if (isWhitelist) {
      allowedModels.push(from)
    } else {
      modelMappings.push({ from, to })
    }
  }

  return { allowedModels, modelMappings }
}

export function buildModelMappingObject(
  mode: ModelRestrictionMode,
  allowedModels: string[],
  modelMappings: ModelMappingEntry[],
  options?: ModelMappingBuildOptions
): Record<string, string> | null {
  const mapping: Record<string, string> = {}

  if (mode === 'whitelist' || mode === 'combined') {
    for (const model of allowedModels) {
      const normalizedModel = model.trim()
      if (!normalizedModel) continue
      // whitelist 模式的本意是"精确模型列表"，如果用户输入了通配符（如 claude-*），
      // 写入 model_mapping 会导致 GetMappedModel() 把真实模型映射成 "claude-*"，从而转发失败。
      // 因此这里跳过包含通配符的条目。
      if (!normalizedModel.includes('*')) {
        // 直连 Kiro 能被 MapModel 折出同一上游 ID 的写成恒等；折不出来的同步名字写上游 ID。
        const synced = options?.kiroDirect
          ? kiroSyncedUpstreamID(normalizedModel, options.kiroUpstreamIDs)
          : ''
        mapping[normalizedModel] =
          synced && synced !== kiroForwardedModelID(normalizedModel) ? synced : normalizedModel
      }
    }
  }

  if (mode === 'mapping' || mode === 'combined') {
    for (const m of modelMappings) {
      const from = m.from.trim()
      const to = m.to.trim()
      if (!from || !to) continue
      // 校验通配符格式：* 只能放在末尾
      if (!isValidWildcardPattern(from)) {
        console.warn(`[buildModelMappingObject] Invalid wildcard pattern, skipped: ${from}`)
        continue
      }
      // to 不允许包含通配符
      if (to.includes('*')) {
        console.warn(`[buildModelMappingObject] Target model cannot contain a wildcard, skipped: ${from} -> ${to}`)
        continue
      }
      mapping[from] = to
    }
  }

  return Object.keys(mapping).length > 0 ? mapping : null
}
