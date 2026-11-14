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

const mountCell = (extra: Record<string, unknown> | undefined) =>
  mount(AccountIQCheckCell, {
    props: { account: { extra } as any },
    global: {
      stubs: {
        HelpTooltip: { template: '<div><slot name="trigger" /><div class="details"><slot /></div></div>' }
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
})
