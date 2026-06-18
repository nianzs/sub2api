import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import GroupsView from '../GroupsView.vue'

const {
  listGroups,
  getUsageSummary,
  getCapacitySummary,
  getModelsListCandidates,
  showError,
  showSuccess,
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getModelsListCandidates: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      create: vi.fn(),
      update: vi.fn(),
      delete: vi.fn(),
      getAll: vi.fn(),
      updateSortOrder: vi.fn(),
      getUsageSummary,
      getCapacitySummary,
      getModelsListCandidates,
    },
    accounts: {
      list: vi.fn(),
      getById: vi.fn(),
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
  }),
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({}),
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

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: {
    show: {
      type: Boolean,
      default: false,
    },
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

const DataTableStub = defineComponent({
  name: 'DataTable',
  template: '<div><slot name="empty" /></div>',
})

const kiroGroup = () => ({
  id: 1,
  name: 'Kiro group',
  description: '',
  platform: 'kiro',
  rate_multiplier: 1,
  is_exclusive: false,
  status: 'active',
  subscription_type: 'standard',
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  account_count: 0,
  sort_order: 0,
  allow_image_generation: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  image_price_1k: null,
  image_price_2k: null,
  image_price_4k: null,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false,
  messages_dispatch_model_config: null,
  require_oauth_only: false,
  require_privacy_set: false,
  model_routing_enabled: false,
  model_routing: null,
  supported_model_scopes: [],
  mcp_xml_inject: true,
  rpm_limit: 0,
  kiro_cache_emulation_enabled: false,
  kiro_auto_sticky_enabled: true,
  kiro_sticky_session_ttl_seconds: 3600,
  kiro_cache_emulation_ratio: 1,
  kiro_endpoint_mode: 'q',
  models_list_config: null,
})

const mountGroupsView = () =>
  mount(GroupsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>',
        },
        BaseDialog: BaseDialogStub,
        DataTable: DataTableStub,
        Pagination: true,
        ConfirmDialog: true,
        EmptyState: true,
        PlatformIcon: true,
        Icon: true,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        VueDraggable: true,
        Teleport: true,
      },
    },
  })

const getEndpointField = (wrapper: ReturnType<typeof mountGroupsView>) => {
  const endpointLabel = wrapper
    .findAll('label.input-label')
    .find((label) => label.text() === 'admin.groups.kiroCache.endpointMode')

  expect(endpointLabel).toBeTruthy()
  return endpointLabel!.element.parentElement as HTMLElement
}

describe('GroupsView Kiro endpoint selector', () => {
  beforeEach(() => {
    listGroups.mockReset()
    getUsageSummary.mockReset()
    getCapacitySummary.mockReset()
    getModelsListCandidates.mockReset()
    showError.mockReset()
    showSuccess.mockReset()

    listGroups.mockResolvedValue({
      items: [],
      total: 0,
      page: 1,
      page_size: 20,
      pages: 0,
    })
    getUsageSummary.mockResolvedValue([])
    getCapacitySummary.mockResolvedValue([])
    getModelsListCandidates.mockResolvedValue([])
  })

  it('uses the shared Select component for the Kiro endpoint mode in create form', async () => {
    const wrapper = mountGroupsView()
    await flushPromises()

    ;(wrapper.vm as any).createForm.platform = 'kiro'
    ;(wrapper.vm as any).showCreateModal = true
    await flushPromises()

    const field = getEndpointField(wrapper)

    expect(field.querySelector('select.input')).toBeNull()
    expect(field.querySelector('.select-trigger')).not.toBeNull()
  })

  it('uses the shared Select component for the Kiro endpoint mode in edit form', async () => {
    const wrapper = mountGroupsView()
    await flushPromises()

    ;(wrapper.vm as any).editingGroup = kiroGroup()
    ;(wrapper.vm as any).editForm.platform = 'kiro'
    ;(wrapper.vm as any).showEditModal = true
    await flushPromises()

    const field = getEndpointField(wrapper)

    expect(field.querySelector('select.input')).toBeNull()
    expect(field.querySelector('.select-trigger')).not.toBeNull()
  })
})
