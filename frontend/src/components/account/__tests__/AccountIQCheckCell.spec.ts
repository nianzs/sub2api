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

describe('AccountIQCheckCell', () => {
  it('shows a dash when the check is off', () => {
    const wrapper = mountCell({ iq_check_state: { checked_at: '2026-11-14T10:00:00Z', result: 'passed' } })
    expect(wrapper.find('[data-testid="iq-check-off"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="iq-check-status"]').exists()).toBe(false)
  })

  it('maps the last result to a status', () => {
    const at = '2026-11-14T10:00:00Z'
    expect(statusText({ iq_check_enabled: true })).toBe('admin.accounts.iqCheck.status.pending')
    expect(statusText({ iq_check_enabled: true, iq_check_state: { checked_at: at, result: 'passed' } }))
      .toBe('admin.accounts.iqCheck.status.passed')
    expect(statusText({ iq_check_enabled: true, iq_check_state: { checked_at: at, result: 'failed', consecutive_failures: 1 } }))
      .toBe('admin.accounts.iqCheck.status.suspect {"count":1,"limit":2}')
    expect(statusText({ iq_check_enabled: true, iq_check_state: { checked_at: at, result: 'failed', consecutive_failures: 2, disabled_by_check: true } }))
      .toBe('admin.accounts.iqCheck.status.disabled')
    expect(statusText({ iq_check_enabled: true, iq_check_state: { checked_at: at, result: 'unknown' } }))
      .toBe('admin.accounts.iqCheck.status.unknown')
    expect(statusText({ iq_check_enabled: true, iq_check_state: { checked_at: at, result: 'unknown', consecutive_failures: 1 } }))
      .toBe('admin.accounts.iqCheck.status.suspect {"count":1,"limit":2}')
  })

  it('shows the raw upstream error in the details', () => {
    const wrapper = mountCell({
      iq_check_enabled: true,
      iq_check_state: { checked_at: '2026-11-14T10:00:00Z', result: 'unknown', error: 'upstream 502: bad gateway', correct: 0, total: 2 }
    })
    expect(wrapper.get('[data-testid="iq-check-error"]').text()).toContain('upstream 502: bad gateway')
  })
})
