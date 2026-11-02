import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import PlatformTypeBadge from '../PlatformTypeBadge.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

describe('PlatformTypeBadge', () => {
  it('labels BPS OAuth accounts as their own type', () => {
    const wrapper = mount(PlatformTypeBadge, {
      props: {
        platform: 'openai',
        type: 'bps-oauth'
      }
    })

    expect(wrapper.text()).toContain('BPS OAuth')
    expect(wrapper.text()).not.toContain('bps-oauth')
  })

  it('shows the privacy badge on OpenAI BPS OAuth accounts', () => {
    const wrapper = mount(PlatformTypeBadge, {
      props: {
        platform: 'openai',
        type: 'bps-oauth',
        privacyMode: 'training_off'
      }
    })

    expect(wrapper.text()).toContain('Private')
  })

  it('uses Kiro theme instead of Anthropic orange theme', () => {
    const wrapper = mount(PlatformTypeBadge, {
      props: {
        platform: 'kiro',
        type: 'oauth',
        planType: 'KIRO FREE'
      }
    })

    expect(wrapper.html()).toContain('bg-violet-100')
    expect(wrapper.html()).toContain('text-violet-700')
    expect(wrapper.html()).not.toContain('bg-orange-100')
  })

  it('shows an English Telemetry badge when enabled', () => {
    const wrapper = mount(PlatformTypeBadge, {
      props: {
        platform: 'openai',
        type: 'oauth',
        planType: 'pro',
        telemetryEnabled: true
      }
    })

    expect(wrapper.get('[data-testid="platform-telemetry-badge"]').text()).toBe('Telemetry')
    expect(wrapper.text()).toContain('Pro 200')
    expect(wrapper.text()).not.toMatch(/抗降智/)
  })

  it('hides the Telemetry badge when the switch is off', () => {
    const wrapper = mount(PlatformTypeBadge, {
      props: {
        platform: 'openai',
        type: 'oauth',
        planType: 'plus'
      }
    })

    expect(wrapper.find('[data-testid="platform-telemetry-badge"]').exists()).toBe(false)
  })

  it('shows Leonardo plan badge with brand indigo theme', () => {
    const wrapper = mount(PlatformTypeBadge, {
      props: {
        platform: 'leonardo',
        type: 'oauth',
        planType: 'Ultimate'
      }
    })

    expect(wrapper.text()).toContain('Ultimate')
    expect(wrapper.html()).toContain('bg-indigo-100')
    expect(wrapper.html()).toContain('text-indigo-700')
  })

  it('keeps Leonardo free plan on muted gray', () => {
    const wrapper = mount(PlatformTypeBadge, {
      props: {
        platform: 'leonardo',
        type: 'oauth',
        planType: 'Free'
      }
    })

    expect(wrapper.text()).toContain('Free')
    expect(wrapper.html()).toContain('bg-gray-100')
    expect(wrapper.html()).not.toContain('bg-indigo-100')
  })

  it('shows Kiro overages tag next to the plan tag when enabled', () => {
    const wrapper = mount(PlatformTypeBadge, {
      props: {
        platform: 'kiro',
        type: 'oauth',
        planType: 'KIRO PRO+',
        overagesEnabled: true
      }
    })

    expect(wrapper.text()).toContain('KIRO PRO+')
    expect(wrapper.text()).toContain('admin.accounts.status.overageActive')
    expect(wrapper.html()).toContain('bg-amber-100')
  })

  it('does not show overages tag for non-Kiro accounts', () => {
    const wrapper = mount(PlatformTypeBadge, {
      props: {
        platform: 'openai',
        type: 'oauth',
        planType: 'Pro',
        overagesEnabled: true
      }
    })

    expect(wrapper.text()).not.toContain('admin.accounts.status.overageActive')
  })
})
