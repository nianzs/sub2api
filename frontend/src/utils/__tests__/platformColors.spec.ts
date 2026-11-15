import { describe, expect, it } from 'vitest'
import {
  platformAccentColor,
  platformBadgeClass,
  platformGradientClass,
  platformLabel,
  platformTextClass
} from '../platformColors'

describe('platformColors', () => {
  it('Kiro 平台使用独立紫色主题，不复用 Anthropic 橙色', () => {
    expect(platformBadgeClass('kiro')).toContain('violet')
    expect(platformTextClass('kiro')).toContain('violet')
    expect(platformGradientClass('kiro')).toContain('from-violet-500')
    expect(platformGradientClass('kiro')).toContain('to-fuchsia-500')
    expect(platformBadgeClass('kiro')).not.toContain('orange')
  })

  it('Fish 平台使用自己的青色主题，而不是默认样式', () => {
    expect(platformBadgeClass('fish')).toContain('cyan')
    expect(platformBadgeClass('fish')).not.toBe(platformBadgeClass('legacy-removed'))
  })


  it('未知平台（如已下线的旧平台字符串）回落到默认样式且不抛错', () => {
    expect(() => platformBadgeClass('legacy-removed')).not.toThrow()
    expect(platformBadgeClass('legacy-removed')).toBeTruthy()
    expect(platformTextClass('legacy-removed')).toBeTruthy()
    expect(platformGradientClass('legacy-removed')).toBeTruthy()
    expect(platformAccentColor('legacy-removed')).toBeTruthy()
    expect(typeof platformLabel('legacy-removed')).toBe('string')
  })
})
