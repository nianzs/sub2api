import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const { listAccounts, getIQCheckDrawings } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  getIQCheckDrawings: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag: vi.fn(),
      getIQCheckDrawings,
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: true, interval_minutes: 30 }),
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn()
    },
    proxies: { getAll: vi.fn().mockResolvedValue([]) },
    groups: { getAll: vi.fn().mockResolvedValue([]) }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token', isSimpleMode: false })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div data-test="data-table">
      <div v-for="row in data" :key="row.id"><slot name="cell-iq_check" :row="row" /></div>
    </div>
  `
}

const IQCellStub = {
  props: ['account', 'drawing'],
  template: `<span data-test="iq-cell" :data-account="account.id" :data-svg="drawing ? drawing.svg : ''" />`
}

function account(id: number, platform: string, extra: Record<string, unknown>) {
  return { id, name: `acc-${id}`, platform, type: 'oauth', status: 'active', schedulable: true, extra }
}

function mountView() {
  return mount(AccountsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
        },
        DataTable: DataTableStub,
        AccountIQCheckCell: IQCellStub,
        AccountTableActions: { template: `<div><button data-test="manual-refresh" @click="$emit('refresh')">refresh</button><slot name="after" /></div>` },
        AccountTableFilters: true,
        AccountBulkActionsBar: true,
        Pagination: true,
        ConfirmDialog: true,
        AccountActionMenu: true,
        ImportDataModal: true,
        ReAuthAccountModal: true,
        AccountTestModal: true,
        AccountStatsModal: true,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        CreateAccountModal: true,
        EditAccountModal: true,
        BulkEditAccountModal: true,
        PlatformTypeBadge: true,
        AccountCapacityCell: true,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: true,
        AccountUsageCell: true,
        HelpTooltip: true,
        Icon: true,
        Teleport: true
      }
    }
  })
}

describe('admin AccountsView IQ check drawings', () => {
  beforeEach(() => {
    localStorage.clear()
    getIQCheckDrawings.mockReset()
    listAccounts.mockReset().mockResolvedValue({
      items: [
        account(1, 'openai', { iq_check_enabled: true, iq_check_state: { checked_at: '2026-11-15T10:00:00Z' } }),
        account(2, 'openai', {}),
        account(3, 'anthropic', { iq_check_enabled: true }),
        account(4, 'openai', { iq_check_state: { checked_at: '2026-11-15T09:00:00Z', disabled_by_check: true } })
      ],
      total: 4,
      page: 1,
      page_size: 20,
      pages: 1
    })
  })

  it('loads drawings only for OpenAI accounts that use the check and passes them to the cell', async () => {
    getIQCheckDrawings.mockResolvedValue([
      { account_id: 1, svg: '<svg xmlns="http://www.w3.org/2000/svg"/>', error: '', model: 'gpt-5.4', latency_ms: 1200, drawn_at: '2026-11-15T10:00:00Z' }
    ])
    const wrapper = mountView()
    await flushPromises()

    expect(getIQCheckDrawings).toHaveBeenCalledTimes(1)
    expect(getIQCheckDrawings).toHaveBeenCalledWith([1, 4])
    expect(wrapper.get('[data-test="iq-cell"][data-account="1"]').attributes('data-svg')).toContain('<svg')
    expect(wrapper.get('[data-test="iq-cell"][data-account="2"]').attributes('data-svg')).toBe('')
  })

  it('keeps the list working when the drawings request fails', async () => {
    getIQCheckDrawings.mockRejectedValue(new Error('boom'))
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.findAll('[data-test="iq-cell"]')).toHaveLength(4)
    expect(wrapper.get('[data-test="iq-cell"][data-account="1"]').attributes('data-svg')).toBe('')
    errorSpy.mockRestore()
  })

  it('reloads only the drawings whose check time changed', async () => {
    getIQCheckDrawings.mockImplementation(async (ids: number[]) => ids.map(id => (
      { account_id: id, svg: `<svg data-v="${getIQCheckDrawings.mock.calls.length}"/>`, error: '', model: 'gpt-5.4', latency_ms: 1, drawn_at: '2026-11-15T10:00:00Z' }
    )))
    const wrapper = mountView()
    await flushPromises()
    expect(getIQCheckDrawings).toHaveBeenLastCalledWith([1, 4])

    listAccounts.mockResolvedValueOnce({
      items: [
        account(1, 'openai', { iq_check_enabled: true, iq_check_state: { checked_at: '2026-11-15T11:00:00Z' } }),
        account(4, 'openai', { iq_check_state: { checked_at: '2026-11-15T09:00:00Z', disabled_by_check: true } })
      ],
      total: 2,
      page: 1,
      page_size: 20,
      pages: 1
    })
    await wrapper.get('[data-test="manual-refresh"]').trigger('click')
    await flushPromises()

    expect(getIQCheckDrawings).toHaveBeenCalledTimes(2)
    expect(getIQCheckDrawings).toHaveBeenLastCalledWith([1])
    expect(wrapper.get('[data-test="iq-cell"][data-account="1"]').attributes('data-svg')).toContain('data-v="2"')
    // The unchanged account keeps the drawing it already had.
    expect(wrapper.get('[data-test="iq-cell"][data-account="4"]').attributes('data-svg')).toContain('data-v="1"')
  })

  it('splits more than 200 accounts into batches the backend accepts', async () => {
    const items = Array.from({ length: 250 }, (_, i) => account(i + 1, 'openai', { iq_check_enabled: true }))
    listAccounts.mockReset().mockResolvedValue({ items, total: 250, page: 1, page_size: 250, pages: 1 })
    getIQCheckDrawings.mockImplementation(async (ids: number[]) => ids.map(id => (
      { account_id: id, svg: '<svg/>', error: '', model: 'gpt-5.4', latency_ms: 1, drawn_at: '2026-11-15T10:00:00Z' }
    )))
    const wrapper = mountView()
    await flushPromises()

    expect(getIQCheckDrawings).toHaveBeenCalledTimes(2)
    expect(getIQCheckDrawings.mock.calls[0][0]).toHaveLength(200)
    expect(getIQCheckDrawings.mock.calls[1][0]).toHaveLength(50)
    expect(wrapper.get('[data-test="iq-cell"][data-account="250"]').attributes('data-svg')).toBe('<svg/>')
  })
})
