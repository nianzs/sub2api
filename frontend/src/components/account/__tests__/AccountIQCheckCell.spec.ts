import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountIQCheckCell from '../AccountIQCheckCell.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => params ? `${key} ${JSON.stringify(params)}` : key
    })
  }
})

const mountCell = (extra: Record<string, unknown> | undefined, drawing: Record<string, unknown> | null = null) =>
  mount(AccountIQCheckCell, {
    props: { account: { extra } as any, drawing: drawing as any },
    global: {
      stubs: {
        HelpTooltip: { template: '<div><slot name="trigger" /><div class="details"><slot /></div></div>' },
        BaseDialog: { props: ['show'], template: '<section v-if="show" data-testid="dialog"><slot /></section>' }
      }
    }
  })

const statusText = (extra: Record<string, unknown> | undefined) =>
  mountCell(extra).find('[data-testid="iq-check-status"]').text()

const at = '2026-11-14T10:00:00Z'
const on = (state?: Record<string, unknown>, auto = false) => ({
  iq_check_enabled: true,
  ...(auto ? { iq_check_auto_disable: true } : {}),
  ...(state ? { iq_check_state: { checked_at: at, ...state } } : {})
})

describe('AccountIQCheckCell', () => {
  it('shows a dash when the check is off', () => {
    const wrapper = mountCell({ iq_check_state: { checked_at: at, result: 'passed' } })
    expect(wrapper.find('[data-testid="iq-check-off"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="iq-check-status"]').exists()).toBe(false)
  })

  it('keeps showing an account the check stopped after the check is turned off', () => {
    const wrapper = mountCell({ iq_check_state: { checked_at: at, result: 'failed', disabled_by_check: true } })
    expect(wrapper.get('[data-testid="iq-check-status"]').text()).toBe('admin.accounts.iqCheck.status.disabled')
    expect(wrapper.get('[data-testid="iq-check-mode"]').text()).toBe('admin.accounts.iqCheck.details.off')
  })

  it('maps the state to a status', () => {
    expect(statusText(on())).toBe('admin.accounts.iqCheck.status.pending')
    expect(statusText(on({ result: 'passed', recent_wrong: 1 }))).toBe('admin.accounts.iqCheck.status.passed')
    expect(statusText(on({ result: 'failed', total: 2, correct: 1, last_round_wrong: 1, recent_wrong: 1 })))
      .toBe('admin.accounts.iqCheck.status.wrong {"wrong":1,"total":2}')
    expect(statusText(on({ result: 'failed', total: 2, correct: 0, last_round_wrong: 2, recent_wrong: 3 })))
      .toBe('admin.accounts.iqCheck.status.triggered')
    expect(statusText(on({ result: 'failed', recent_wrong: 4, disabled_by_check: true }, true)))
      .toBe('admin.accounts.iqCheck.status.disabled')
    expect(statusText(on({ result: 'unknown' }))).toBe('admin.accounts.iqCheck.status.unknown')
  })

  it('shows the mode, accuracy and raw upstream error in the details', () => {
    const wrapper = mountCell(on({
      result: 'unknown', error: 'upstream 502: bad gateway', correct: 0, total: 2, answered: 8, answered_correct: 6
    }, true))
    expect(wrapper.get('[data-testid="iq-check-mode"]').text()).toContain('admin.accounts.iqCheck.modes.auto')
    expect(wrapper.get('[data-testid="iq-check-accuracy"]').text()).toContain('"percent":75')
    expect(wrapper.get('[data-testid="iq-check-error"]').text()).toContain('upstream 502: bad gateway')
  })

  it('shows the latest drawing as an image thumbnail and opens it larger', async () => {
    const svg = '<svg xmlns="http://www.w3.org/2000/svg"><circle r="4"/></svg>'
    const wrapper = mountCell(on({ result: 'passed' }), {
      account_id: 1, svg, error: '', model: 'gpt-5.4', latency_ms: 100700, drawn_at: at
    })
    const thumbnail = wrapper.get('[data-testid="iq-check-thumbnail"]')
    expect(thumbnail.get('img').attributes('src')).toBe(`data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`)
    expect(wrapper.get('[data-testid="iq-check-drawn-at"]').text()).toContain('100.7s')
    expect(wrapper.find('[data-testid="iq-check-drawing-dialog"]').exists()).toBe(false)

    await thumbnail.trigger('click')
    expect(wrapper.get('[data-testid="iq-check-drawing-large"]').attributes('src')).toContain('data:image/svg+xml')
  })

  it('marks a failed drawing and shows its error', async () => {
    const wrapper = mountCell(on({ result: 'passed' }), {
      account_id: 1, svg: '', error: 'upstream 429: rate limited', model: 'gpt-5.4', latency_ms: 3000, drawn_at: at
    })
    expect(wrapper.find('[data-testid="iq-check-thumbnail"] img').exists()).toBe(false)
    expect(wrapper.get('[data-testid="iq-check-drawing-failed"]').text()).toBe('admin.accounts.iqCheck.drawing.failed')
    expect(wrapper.get('[data-testid="iq-check-thumbnail"]').attributes('title')).toContain('upstream 429: rate limited')
    await wrapper.get('[data-testid="iq-check-thumbnail"]').trigger('click')
    expect(wrapper.get('[data-testid="iq-check-drawing-dialog"]').text()).toContain('upstream 429: rate limited')
  })
})
