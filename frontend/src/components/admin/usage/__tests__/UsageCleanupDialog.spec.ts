import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const { listCleanupTasks } = vi.hoisted(() => ({
  listCleanupTasks: vi.fn(),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
  }),
}))

vi.mock('@/api/admin/usage', () => ({
  default: {
    listCleanupTasks,
    createCleanupTask: vi.fn(),
    cancelCleanupTask: vi.fn(),
  },
  adminUsageAPI: {
    listCleanupTasks,
    createCleanupTask: vi.fn(),
    cancelCleanupTask: vi.fn(),
  },
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    usage: {
      searchUsers: vi.fn().mockResolvedValue([]),
      searchApiKeys: vi.fn().mockResolvedValue([]),
    },
    groups: {
      list: vi.fn().mockResolvedValue({ items: [] }),
    },
    dashboard: {
      getModelStats: vi.fn().mockResolvedValue({ models: [] }),
    },
    accounts: {
      list: vi.fn().mockResolvedValue({ items: [] }),
    },
  },
}))

import UsageCleanupDialog from '../UsageCleanupDialog.vue'

describe('UsageCleanupDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listCleanupTasks.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 5 })
  })

  it('把外部模型选项传给清理筛选器', async () => {
    const wrapper = mount(UsageCleanupDialog, {
      props: {
        show: true,
        filters: {},
        startDate: '2026-07-01',
        endDate: '2026-07-01',
        modelOptions: ['claude-opus-4-8', 'gpt-5.4'],
      },
      global: {
        stubs: {
          BaseDialog: {
            props: ['show'],
            template: '<section v-if="show"><slot /><slot name="footer" /></section>',
          },
          ConfirmDialog: true,
          Pagination: true,
          UsageFilters: {
            props: ['modelOptions'],
            template: '<div data-test="model-options">{{ modelOptions.join(",") }}</div>',
          },
        },
      },
    })

    await flushPromises()

    expect(wrapper.get('[data-test="model-options"]').text()).toBe('claude-opus-4-8,gpt-5.4')
  })
})
