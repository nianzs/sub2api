import { describe, expect, it, vi } from 'vitest'

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn()
}))

import {
  appendKiroSyncedMappings,
  buildModelMappingObject,
  fetchKiroDefaultMappings,
  getModelsByPlatform,
  getPresetMappingsByPlatform,
  kiroUpstreamModelID,
  splitModelMappingObject
} from '../useModelWhitelist'
import { BUILTIN_PLATFORM_CATALOG, resetPlatformCatalog, setPlatformCatalog } from '@/constants/platformCatalog'

describe('useModelWhitelist', () => {
  it('平台清单中没有内置模型列表的多协议供应商不预填白名单', () => {
    setPlatformCatalog({
      ...BUILTIN_PLATFORM_CATALOG,
      platforms: [
        ...BUILTIN_PLATFORM_CATALOG.platforms,
        {
          id: 'acme_router',
          display_name: 'Acme Router',
          gateway: 'openai',
          cn_provider: false,
          multi_protocol: {
            default_mode: 'standard',
            routing: 'by_model',
            modes: [{ mode: 'standard', base_urls: { chat_completions: 'https://api.acme-router.example/v1' } }]
          }
        }
      ]
    })
    try {
      expect(getModelsByPlatform('acme_router')).toEqual([])
      expect(getModelsByPlatform('kimi').length).toBeGreaterThan(0)
      // 非多协议供应商的未知平台维持原有回退。
      expect(getModelsByPlatform('bedrock')).toEqual(getModelsByPlatform('anthropic'))
    } finally {
      resetPlatformCatalog()
    }
  })

  it('openai 模型列表包含 GPT-5.4 官方快照', () => {
    const models = getModelsByPlatform('openai')

    expect(models).toContain('gpt-5.4')
    expect(models).toContain('gpt-5.4-mini')
    expect(models).toContain('gpt-5.4-2026-03-05')
    expect(models).toContain('codex-auto-review')
    expect(models).toContain('gpt-5.6')
    expect(models).toContain('gpt-6')
    expect(models).toContain('gpt-6-astra')
    expect(models).toContain('gpt-6.1-sol')
    expect(models).toContain('gpt-6.1-terra')
    expect(models).toContain('gpt-6-sol')
    expect(models).toContain('gpt-6-luna')
  })

  it('openai 预设映射包含 GPT-6 别名和 Astra', () => {
    expect(getPresetMappingsByPlatform('openai')).toEqual(expect.arrayContaining([
      expect.objectContaining({ label: 'GPT-6.1 Sol', from: 'gpt-6.1-sol', to: 'gpt-6.1-sol' }),
      expect.objectContaining({ label: 'GPT-6.1 Terra', from: 'gpt-6.1-terra', to: 'gpt-6.1-terra' }),
      expect.objectContaining({ label: 'GPT-6', from: 'gpt-6', to: 'gpt-6' }),
      expect.objectContaining({ label: 'GPT-6 Astra', from: 'gpt-6-astra', to: 'gpt-6-astra' })
    ]))
  })

  it('openai 模型列表不再暴露已下线的 ChatGPT 登录 Codex 模型', () => {
    const models = getModelsByPlatform('openai')

    expect(models).not.toContain('gpt-5')
    expect(models).not.toContain('gpt-5.1')
    expect(models).not.toContain('gpt-5.1-codex')
    expect(models).not.toContain('gpt-5.1-codex-max')
    expect(models).not.toContain('gpt-5.1-codex-mini')
    expect(models).not.toContain('gpt-5.2-codex')
  })

  it('antigravity 模型列表包含图片模型兼容项', () => {
    const models = getModelsByPlatform('antigravity')

    expect(models).toContain('gemini-2.5-flash-image')
    expect(models).toContain('gemini-3.1-flash-image')
    expect(models).toContain('gemini-3-pro-image')
  })

  it('Claude 模型列表包含新发布的 Claude 模型', () => {
    expect(getModelsByPlatform('claude')).toContain('claude-fable-5-1')
    expect(getModelsByPlatform('antigravity')).toContain('claude-fable-5-1')
    expect(getModelsByPlatform('claude')).toContain('claude-fable-5')
    expect(getModelsByPlatform('antigravity')).toContain('claude-fable-5')
    expect(getModelsByPlatform('claude')).toContain('claude-opus-5-5')
    expect(getModelsByPlatform('antigravity')).not.toContain('claude-opus-5-5')
    expect(getModelsByPlatform('claude')).toContain('claude-sonnet-5-5')
    expect(getModelsByPlatform('antigravity')).not.toContain('claude-sonnet-5-5')
    expect(getModelsByPlatform('claude')).toContain('claude-opus-4-8')
    expect(getModelsByPlatform('antigravity')).toContain('claude-opus-4-8')
  })

  it('Claude Sonnet 5.5 预设使用各平台的官方模型 ID', () => {
    expect(getPresetMappingsByPlatform('claude')).toEqual(expect.arrayContaining([
      expect.objectContaining({ label: 'Sonnet 5.5', from: 'claude-sonnet-5-5', to: 'claude-sonnet-5-5' })
    ]))
    expect(getPresetMappingsByPlatform('bedrock')).toEqual(expect.arrayContaining([
      expect.objectContaining({ label: 'Sonnet 5.5', from: 'claude-sonnet-5-5', to: 'global.anthropic.claude-sonnet-5-5' })
    ]))
  })

  it('Claude 默认白名单保存后允许 Haiku 5.5，预设使用同一模型 ID', () => {
    const models: string[] = getModelsByPlatform('anthropic')
    const mapping: Record<string, string> | null = buildModelMappingObject('whitelist', models, [])

    expect(mapping?.['claude-haiku-5-5']).toBe('claude-haiku-5-5')
    expect(getPresetMappingsByPlatform('anthropic')).toEqual(expect.arrayContaining([
      expect.objectContaining({ label: 'Haiku 5.5', from: 'claude-haiku-5-5', to: 'claude-haiku-5-5' })
    ]))
  })

  it('xAI 模型列表包含 Grok 4.5 官方模型和别名', () => {
    const models = getModelsByPlatform('grok')

    expect(models).toContain('grok-4.6')
    expect(models).toContain('grok-4.6-latest')
    expect(models).toContain('grok-4.5')
    expect(models).toContain('grok-4.5-latest')
    expect(models).toContain('grok-build-latest')
    expect(models).toContain('grok-imagine-image-2.0')
    expect(models).toContain('grok-imagine-video-1.5')
  })

  it('combined 模式支持 Grok 4.5 官方别名映射', () => {
    const mapping = buildModelMappingObject(
      'combined',
      ['grok-4.5'],
      [
        { from: 'grok-latest', to: 'grok-4.5' },
        { from: 'grok-4.5-latest', to: 'grok-4.5' },
        { from: 'grok-build-latest', to: 'grok-4.5' }
      ]
    )

    expect(mapping).toEqual({
      'grok-4.5': 'grok-4.5',
      'grok-latest': 'grok-4.5',
      'grok-4.5-latest': 'grok-4.5',
      'grok-build-latest': 'grok-4.5'
    })
  })

  it('grok 模型列表包含 Composer 默认项和兼容别名', () => {
    const models = getModelsByPlatform('grok')

    expect(models).toContain('grok-composer-2.5-fast')
    expect(models).not.toContain('grok-composer')
    expect(models).toContain('composer-2.5')
  })

  it('gemini 模型列表包含原生生图模型', () => {
    const models = getModelsByPlatform('gemini')

    expect(models).toContain('gemini-3.1-flash-image')
    expect(models).toContain('gemini-3-pro-image')
    expect(models.indexOf('gemini-3.1-flash-image')).toBeLessThan(models.indexOf('gemini-3-flash'))
    expect(models.indexOf('gemini-3-pro-image')).toBeLessThan(models.indexOf('gemini-3.5-flash'))
  })

  it('gemini 模型列表不含已停用的 2.x 模型与 Antigravity 档位别名', () => {
    const models = getModelsByPlatform('gemini')

    for (const model of ['gemini-2.0-flash', 'gemini-2.5-flash', 'gemini-2.5-pro', 'gemini-2.5-flash-image', 'gemini-3-pro-preview']) {
      expect(models).not.toContain(model)
    }
    for (const model of ['gemini-3.1-pro', 'gemini-3.1-pro-high', 'gemini-3.1-pro-low']) {
      expect(models).not.toContain(model)
    }
    expect(models).toContain('gemini-3.8-flash')
  })

  it('antigravity 模型列表会把新的 Gemini 图片模型排在前面', () => {
    const models = getModelsByPlatform('antigravity')

    expect(models.indexOf('gemini-3.1-flash-image')).toBeLessThan(models.indexOf('gemini-2.5-flash'))
    expect(models.indexOf('gemini-2.5-flash-image')).toBeLessThan(models.indexOf('gemini-2.5-flash-lite'))
  })

  it('kiro 模型列表不暴露旧的 -agentic / -chat 后缀', () => {
    const models = getModelsByPlatform('kiro')

    expect(models).toContain('claude-sonnet-4-6')
    expect(models).toContain('claude-sonnet-4-6-thinking')
    expect(models).toContain('claude-opus-4-8')
    expect(models).toContain('claude-opus-4-8-thinking')
    expect(models).not.toContain('claude-sonnet-4-6-chat')
    expect(models.every((model) => !model.endsWith('-agentic') && !model.endsWith('-chat'))).toBe(true)
  })

  it('kiro 模型列表包含 Claude 和 GPT-5.6 精确模型', () => {
    const models = getModelsByPlatform('kiro')

    expect(models).toEqual([
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
    ])
    expect(models).toContain('gpt-5.6-sol')
    expect(models).toContain('gpt-5.6-terra')
    expect(models).toContain('gpt-5.6-luna')
    expect(models).toContain('codex-auto-review')
    expect(models).not.toContain('gpt-5.6')
    expect(models.some(model => model.endsWith('-agentic'))).toBe(false)
    expect(models.some(model => model.endsWith('-chat'))).toBe(false)
    expect(models).not.toContain('kiro-auto')
    expect(models).not.toContain('claude-opus-4.7')
    expect(models).not.toContain('claude-opus-4.6')
    expect(models).not.toContain('claude-sonnet-4.6')
    expect(models).not.toContain('claude-3-5-sonnet-20241022')
    expect(models).not.toContain('claude-3-5-haiku-20241022')
    expect(models).not.toContain('claude-haiku-4.5')
    expect(models).not.toContain('gpt-4o')
    expect(models).not.toContain('gpt-4')
    expect(models).not.toContain('gpt-4-turbo')
    expect(models).not.toContain('gpt-3.5-turbo')
    expect(models).not.toContain('deepseek-3-2')
    expect(models).not.toContain('minimax-m2-1')
    expect(models).not.toContain('qwen3-coder-next')
  })

  it('claude 模型列表包含 dated 和 thinking 兼容别名', () => {
    const models = getModelsByPlatform('claude')

    expect(models).toContain('claude-opus-4-6-thinking')
    expect(models).toContain('claude-opus-4-5-20251101-thinking')
    expect(models).toContain('claude-sonnet-4-20250514-thinking')
    expect(models).toContain('claude-haiku-4-5-20251001-thinking')
  })

  it('antigravity 模型列表包含 Gemini 3.1 Pro 通用别名', () => {
    const models = getModelsByPlatform('antigravity')

    expect(models).toContain('gemini-3.1-pro')
  })

  it('Gemini 4 Argon 在 gemini 与 antigravity 平台使用对应真实模型 ID 与预设映射', () => {
    expect(getModelsByPlatform('gemini')).toContain('gemini-4-argon')
    const agModels = getModelsByPlatform('antigravity')
    expect(agModels).not.toContain('gemini-4-argon')
    expect(agModels).toContain('gemini-4-argon-high')
    expect(agModels).toContain('gemini-4-argon-medium')
    expect(agModels).toContain('gemini-4-argon-low')
    expect(getPresetMappingsByPlatform('antigravity')).toEqual(expect.arrayContaining([
      expect.objectContaining({ from: 'gemini-4-argon', to: 'gemini-4-argon-high' }),
      expect.objectContaining({ from: 'gemini-4-argon-high', to: 'gemini-4-argon-high' }),
      expect.objectContaining({ from: 'gemini-4-argon-medium', to: 'gemini-4-argon-medium' }),
      expect.objectContaining({ from: 'gemini-4-argon-low', to: 'gemini-4-argon-low' })
    ]))
  })

  it('whitelist 模式会忽略通配符条目', () => {
    const mapping = buildModelMappingObject('whitelist', ['claude-*', 'gemini-3.1-flash-image'], [])
    expect(mapping).toEqual({
      'gemini-3.1-flash-image': 'gemini-3.1-flash-image'
    })
  })

  it('whitelist 模式会保留 GPT-5.4 官方快照的精确映射', () => {
    const mapping = buildModelMappingObject('whitelist', ['gpt-5.4-2026-03-05'], [])

    expect(mapping).toEqual({
      'gpt-5.4-2026-03-05': 'gpt-5.4-2026-03-05'
    })
  })

  it('whitelist keeps GPT-5.4 mini exact mappings', () => {
    const mapping = buildModelMappingObject('whitelist', ['gpt-5.4-mini'], [])

    expect(mapping).toEqual({
      'gpt-5.4-mini': 'gpt-5.4-mini'
    })
  })

  it('kiro 预设映射暴露 Claude 和 GPT-5.6 精确入口', () => {
    const mappings = getPresetMappingsByPlatform('kiro')
    const mappingPairs = mappings.map(({ from, to }) => ({ from, to }))
    const mappingTargets = mappings.map(item => item.to)

    expect(mappingPairs).toEqual([
      { from: 'gpt-5.6-sol', to: 'gpt-5.6-sol' },
      { from: 'gpt-5.6-terra', to: 'gpt-5.6-terra' },
      { from: 'gpt-5.6-luna', to: 'gpt-5.6-luna' },
      { from: 'kimi-k3', to: 'kimi-k3' },
      { from: 'codex-auto-review', to: 'gpt-5.6-luna' },
      { from: 'claude-opus-4-8', to: 'claude-opus-4.8' },
      { from: 'claude-opus-4-8-thinking', to: 'claude-opus-4.8' },
      { from: 'claude-opus-4-7', to: 'claude-opus-4.7' },
      { from: 'claude-opus-4-7-thinking', to: 'claude-opus-4.7' },
      { from: 'claude-opus-4-6', to: 'claude-opus-4.6' },
      { from: 'claude-opus-4-6-thinking', to: 'claude-opus-4.6' },
      { from: 'claude-opus-5', to: 'claude-opus-5' },
      { from: 'claude-opus-5-thinking', to: 'claude-opus-5' },
      { from: 'claude-opus-5-5', to: 'claude-opus-5.5' },
      { from: 'claude-opus-5-5-thinking', to: 'claude-opus-5.5' },
      { from: 'claude-sonnet-5', to: 'claude-sonnet-5' },
      { from: 'claude-sonnet-5-thinking', to: 'claude-sonnet-5' },
      { from: 'claude-sonnet-5-5', to: 'claude-sonnet-5.5' },
      { from: 'claude-sonnet-5-5-thinking', to: 'claude-sonnet-5.5' },
      { from: 'claude-sonnet-4-6', to: 'claude-sonnet-4.6' },
      { from: 'claude-sonnet-4-6-thinking', to: 'claude-sonnet-4.6' },
      { from: 'claude-opus-4-5-20251101', to: 'claude-opus-4.5' },
      { from: 'claude-opus-4-5-20251101-thinking', to: 'claude-opus-4.5' },
      { from: 'claude-sonnet-4-5-20250929', to: 'claude-sonnet-4.5' },
      { from: 'claude-sonnet-4-5-20250929-thinking', to: 'claude-sonnet-4.5' },
      { from: 'claude-haiku-4-5-20251001', to: 'claude-haiku-4.5' },
      { from: 'claude-haiku-4-5-20251001-thinking', to: 'claude-haiku-4.5' },
      { from: 'claude-haiku-5-5', to: 'claude-haiku-5.5' },
      { from: 'claude-haiku-5-5-thinking', to: 'claude-haiku-5.5' }
    ])
    expect(mappingPairs).toEqual(expect.arrayContaining([
      { from: 'gpt-5.6-sol', to: 'gpt-5.6-sol' },
      { from: 'gpt-5.6-terra', to: 'gpt-5.6-terra' },
      { from: 'gpt-5.6-luna', to: 'gpt-5.6-luna' },
      { from: 'codex-auto-review', to: 'gpt-5.6-luna' }
    ]))
    expect(mappingTargets).not.toContain('gpt-5.6')
    expect(mappings.some(item => item.from === 'gpt-5.6')).toBe(false)
    expect(mappingTargets.some(model => model.endsWith('-agentic'))).toBe(false)
    expect(mappingTargets.some(model => model.endsWith('-chat'))).toBe(false)
    expect(mappingTargets).not.toContain('kiro-auto')
    expect(mappingTargets.some(model => model.startsWith('kiro-'))).toBe(false)
    expect(mappings.some(item => item.from === 'claude-opus-4.7')).toBe(false)
    expect(mappings.some(item => item.from === 'claude-opus-4.6')).toBe(false)
    expect(mappings.some(item => item.from === 'claude-sonnet-4.6')).toBe(false)
    expect(mappings.some(item => item.from === 'claude-3-5-sonnet-20241022')).toBe(false)
    expect(mappings.some(item => item.from === 'claude-3-5-haiku-20241022')).toBe(false)
    expect(mappings.some(item => item.from === 'claude-haiku-4.5')).toBe(false)
    expect(mappingTargets).not.toContain('gpt-4o')
    expect(mappingTargets).not.toContain('gpt-4')
    expect(mappingTargets).not.toContain('gpt-4-turbo')
    expect(mappingTargets).not.toContain('gpt-3.5-turbo')
    expect(mappingTargets).not.toContain('deepseek-3.2')
    expect(mappingTargets).not.toContain('minimax-m2.1')
    expect(mappingTargets).not.toContain('qwen3-coder-next')
  })

  it('kiro 默认映射会在前端填充所有可精确定价模型', async () => {
    const mappings = await fetchKiroDefaultMappings()

    expect(mappings).toEqual(expect.arrayContaining([
      { from: 'gpt-5.6-sol', to: 'gpt-5.6-sol' },
      { from: 'gpt-5.6-terra', to: 'gpt-5.6-terra' },
      { from: 'gpt-5.6-luna', to: 'gpt-5.6-luna' },
      { from: 'kimi-k3', to: 'kimi-k3' },
      { from: 'codex-auto-review', to: 'gpt-5.6-luna' },
      { from: 'claude-opus-4-8', to: 'claude-opus-4.8' },
      { from: 'claude-opus-4-8-thinking', to: 'claude-opus-4.8' },
      { from: 'claude-opus-4-7', to: 'claude-opus-4.7' },
      { from: 'claude-opus-4-7-thinking', to: 'claude-opus-4.7' },
      { from: 'claude-opus-4-6', to: 'claude-opus-4.6' },
      { from: 'claude-opus-4-6-thinking', to: 'claude-opus-4.6' },
      { from: 'claude-opus-5', to: 'claude-opus-5' },
      { from: 'claude-opus-5-thinking', to: 'claude-opus-5' },
      { from: 'claude-opus-5-5', to: 'claude-opus-5.5' },
      { from: 'claude-opus-5-5-thinking', to: 'claude-opus-5.5' },
      { from: 'claude-sonnet-5', to: 'claude-sonnet-5' },
      { from: 'claude-sonnet-5-thinking', to: 'claude-sonnet-5' },
      { from: 'claude-sonnet-5-5', to: 'claude-sonnet-5.5' },
      { from: 'claude-sonnet-5-5-thinking', to: 'claude-sonnet-5.5' },
      { from: 'claude-sonnet-4-6', to: 'claude-sonnet-4.6' },
      { from: 'claude-sonnet-4-6-thinking', to: 'claude-sonnet-4.6' },
      { from: 'claude-opus-4-5-20251101', to: 'claude-opus-4.5' },
      { from: 'claude-opus-4-5-20251101-thinking', to: 'claude-opus-4.5' },
      { from: 'claude-sonnet-4-5-20250929', to: 'claude-sonnet-4.5' },
      { from: 'claude-sonnet-4-5-20250929-thinking', to: 'claude-sonnet-4.5' },
      { from: 'claude-haiku-4-5-20251001', to: 'claude-haiku-4.5' },
      { from: 'claude-haiku-4-5-20251001-thinking', to: 'claude-haiku-4.5' },
      { from: 'claude-haiku-5-5', to: 'claude-haiku-5.5' },
      { from: 'claude-haiku-5-5-thinking', to: 'claude-haiku-5.5' }
    ]))
    expect(mappings).toHaveLength(29)
    expect(mappings.every(item => !item.from.startsWith('kiro-'))).toBe(true)
    expect(mappings.every(item => !item.to.startsWith('kiro-'))).toBe(true)
    expect(mappings.every(item => !item.from.endsWith('-agentic'))).toBe(true)
    expect(mappings.every(item => !item.to.endsWith('-agentic'))).toBe(true)
    expect(mappings.every(item => !item.from.endsWith('-chat'))).toBe(true)
    expect(mappings.every(item => !item.to.endsWith('-chat'))).toBe(true)
    expect(mappings.some(item => item.from === 'gpt-5.6')).toBe(false)
    expect(mappings.some(item => item.to === 'gpt-5.6')).toBe(false)
    expect(mappings.some(item => item.to === 'claude-opus-4-7')).toBe(false)
  })

  it('kiro Sonnet 5.5 四种写法解析到同一上游 ID，直连白名单存恒等', () => {
    for (const name of [
      'claude-sonnet-5-5',
      'claude-sonnet-5-5-thinking',
      'claude-sonnet-5.5',
      'claude-sonnet-5.5-thinking'
    ]) {
      expect(kiroUpstreamModelID(name)).toBe('claude-sonnet-5.5')
    }

    const mapping = buildModelMappingObject('whitelist', ['claude-sonnet-5-5'], [], {
      kiroDirect: true
    })
    expect(mapping).toEqual({ 'claude-sonnet-5-5': 'claude-sonnet-5-5' })
  })

  it('映射支持通配符前缀，且目标模型不允许带通配符', () => {
    expect(
      buildModelMappingObject('mapping', [], [{ from: 'gpt-image-*', to: 'gpt-image-2' }])
    ).toEqual({ 'gpt-image-*': 'gpt-image-2' })

    expect(
      buildModelMappingObject('mapping', [], [{ from: 'gpt-image-2', to: 'gpt-image-*' }])
    ).toBeNull()
  })

  it('combined 模式会同时保留白名单身份映射和模型映射', () => {
    const mapping = buildModelMappingObject(
      'combined',
      ['gpt-5.4', 'claude-*'],
      [
        { from: 'gpt-latest', to: 'gpt-5.4' },
        { from: 'gpt-5.4', to: 'gpt-5.4-mini' }
      ]
    )

    expect(mapping).toEqual({
      'gpt-5.4': 'gpt-5.4-mini',
      'gpt-latest': 'gpt-5.4'
    })
  })

  it('kiro 新模型用同步带回的上游 ID，未知名字不再猜点号', () => {
    expect(kiroUpstreamModelID('claude-opus-4-8')).toBe('claude-opus-4.8')
    expect(kiroUpstreamModelID('claude-opus-4-5-20251101')).toBe('claude-opus-4.5')
    expect(kiroUpstreamModelID('claude-opus-4-9')).toBe('claude-opus-4-9')
    expect(kiroUpstreamModelID('claude-opus-4-9', { 'claude-opus-4-9': 'claude-opus-4.9' })).toBe('claude-opus-4.9')

    const mapping = buildModelMappingObject(
      'whitelist',
      ['claude-opus-4-9', 'claude-opus-4-8'],
      [],
      { kiroDirect: true, kiroUpstreamIDs: { 'claude-opus-4-9': 'claude-opus-4.9' } }
    )
    expect(mapping).toEqual({
      'claude-opus-4-9': 'claude-opus-4-9',
      'claude-opus-4-8': 'claude-opus-4-8'
    })
  })

  it('kiro 直连白名单保存为恒等映射，自定义行盖过同名白名单', () => {
    const mapping = buildModelMappingObject(
      'combined',
      ['claude-opus-4-5', 'claude-sonnet-4-thinking'],
      [{ from: 'claude-opus-4-5', to: 'claude-sonnet-4.6' }],
      { kiroDirect: true }
    )
    expect(mapping).toEqual({
      'claude-opus-4-5': 'claude-sonnet-4.6',
      'claude-sonnet-4-thinking': 'claude-sonnet-4-thinking'
    })
  })

  it('kiro 旧的点号行归回白名单，不同模型仍留在映射', () => {
    const parsed = splitModelMappingObject(
      {
        'claude-haiku-4-5': 'claude-haiku-4.5',
        'claude-haiku-4-5-thinking': 'claude-haiku-4.5',
        'claude-opus-4-5': 'claude-opus-4.5',
        'claude-opus-4-5-thinking': 'claude-opus-4.5',
        'claude-sonnet-4-5': 'claude-sonnet-4.5',
        'claude-sonnet-4-5-thinking': 'claude-sonnet-4.5',
        'claude-sonnet-4-thinking': 'claude-sonnet-4',
        'claude-haiku-4-5-20251001': 'claude-haiku-4.5',
        'claude-opus-4-8': 'claude-sonnet-4.6'
      },
      { kiroDirect: true }
    )
    expect(parsed.allowedModels).toEqual([
      'claude-haiku-4-5',
      'claude-haiku-4-5-thinking',
      'claude-opus-4-5',
      'claude-opus-4-5-thinking',
      'claude-sonnet-4-5',
      'claude-sonnet-4-5-thinking',
      'claude-sonnet-4-thinking',
      'claude-haiku-4-5-20251001'
    ])
    expect(parsed.modelMappings).toEqual([{ from: 'claude-opus-4-8', to: 'claude-sonnet-4.6' }])

    const remapped = splitModelMappingObject(
      { 'claude-opus-4-5': 'claude-sonnet-4.6' },
      { kiroDirect: true }
    )
    expect(remapped.allowedModels).toEqual([])
    expect(remapped.modelMappings).toEqual([{ from: 'claude-opus-4-5', to: 'claude-sonnet-4.6' }])
  })

  it('kiro 后端折不出来的写法差异仍留在映射', () => {
    const parsed = splitModelMappingObject(
      {
        'claude-3-7-sonnet': 'claude-3.7-sonnet',
        'gpt-5-6-sol': 'gpt-5.6-sol',
        'claude-opus-4-6': 'claude-opus-4.6-thinking'
      },
      { kiroDirect: true }
    )
    expect(parsed.allowedModels).toEqual([])
    expect(parsed.modelMappings).toEqual([
      { from: 'claude-3-7-sonnet', to: 'claude-3.7-sonnet' },
      { from: 'gpt-5-6-sol', to: 'gpt-5.6-sol' },
      { from: 'claude-opus-4-6', to: 'claude-opus-4.6-thinking' }
    ])
  })

  it('kiro 同步白名单只在后端折不出来时写上游 ID，往返不变', () => {
    const options = {
      kiroDirect: true,
      kiroUpstreamIDs: {
        'claude-3-7-sonnet': 'claude-3.7-sonnet',
        'claude-opus-4-9': 'claude-opus-4.9'
      }
    }
    const mapping = buildModelMappingObject(
      'combined',
      ['claude-3-7-sonnet', 'claude-opus-4-9'],
      [],
      options
    )
    expect(mapping).toEqual({
      'claude-3-7-sonnet': 'claude-3.7-sonnet',
      'claude-opus-4-9': 'claude-opus-4-9'
    })

    const parsed = splitModelMappingObject(mapping, { kiroDirect: true })
    expect(parsed.allowedModels).toEqual(['claude-opus-4-9'])
    expect(
      buildModelMappingObject('combined', parsed.allowedModels, parsed.modelMappings, { kiroDirect: true })
    ).toEqual(mapping)
  })

  it('kiro 同步只追加还没有的 from，目标用上游 ID', () => {
    const applied = appendKiroSyncedMappings(
      [{ from: 'claude-opus-4-8', to: 'claude-sonnet-4.6' }],
      ['claude-opus-4-8', 'claude-opus-4-9', 'claude-opus-4-9-thinking'],
      {
        'claude-opus-4-9': 'claude-opus-4.9',
        'claude-opus-4-9-thinking': 'claude-opus-4.9'
      }
    )

    expect(applied.added).toBe(2)
    expect(applied.rows).toEqual([
      { from: 'claude-opus-4-8', to: 'claude-sonnet-4.6' },
      { from: 'claude-opus-4-9', to: 'claude-opus-4.9' },
      { from: 'claude-opus-4-9-thinking', to: 'claude-opus-4.9' }
    ])
  })

  it('combined mode retains a mapping when a whitelist entry has the same source', () => {
    expect(buildModelMappingObject('combined', ['gpt-latest'], [{ from: 'gpt-latest', to: 'deepseek-chat' }])).toEqual({
      'gpt-latest': 'deepseek-chat'
    })
  })

  it('split mapping keeps only identity entries in the whitelist after reopening', () => {
    expect(splitModelMappingObject({ 'gpt-latest': 'deepseek-chat', 'gpt-5.4': 'gpt-5.4' })).toEqual({
      allowedModels: ['gpt-5.4'],
      modelMappings: [{ from: 'gpt-latest', to: 'deepseek-chat' }]
    })
  })

  it('splitModelMappingObject 会把身份映射还原成白名单，其余保留为映射', () => {
    const parsed = splitModelMappingObject({
      'gpt-5.4': 'gpt-5.4',
      'gpt-latest': 'gpt-5.4',
      ' ': 'gpt-empty',
      broken: 123
    })

    expect(parsed).toEqual({
      allowedModels: ['gpt-5.4'],
      modelMappings: [{ from: 'gpt-latest', to: 'gpt-5.4' }]
    })
  })
})
