import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  BUILTIN_PLATFORM_CATALOG,
  resetPlatformCatalog,
  setPlatformCatalog,
} from '@/constants/platformCatalog'

const {
  createAccountMock,
  probeUpstreamBillingMock,
  syncUpstreamModelsMock,
  showWarningMock,
  importCodexSessionMock,
  createOpenAICodexPATMock,
  generateKiroIDCAuthUrlMock,
  generateAuthUrlMock,
  exchangeCodeMock,
  authIsSimpleMode,
} = vi.hoisted(() => ({
  createAccountMock: vi.fn(),
  probeUpstreamBillingMock: vi.fn(),
  syncUpstreamModelsMock: vi.fn(),
  showWarningMock: vi.fn(),
  importCodexSessionMock: vi.fn(),
  createOpenAICodexPATMock: vi.fn(),
  generateKiroIDCAuthUrlMock: vi.fn(),
  generateAuthUrlMock: vi.fn(),
  exchangeCodeMock: vi.fn(),
  authIsSimpleMode: { value: true },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showWarning: showWarningMock,
  }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    get isSimpleMode() {
      return authIsSimpleMode.value
    },
  }),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      create: createAccountMock,
      probeUpstreamBilling: probeUpstreamBillingMock,
      syncUpstreamModels: syncUpstreamModelsMock,
      checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false }),
      importCodexSession: importCodexSessionMock,
      createOpenAICodexPAT: createOpenAICodexPATMock,
      generateAuthUrl: generateAuthUrlMock,
      exchangeCode: exchangeCodeMock,
    },
    settings: {
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
      getSettings: vi.fn().mockResolvedValue({}),
    },
    tlsFingerprintProfiles: {
      list: vi.fn().mockResolvedValue([]),
    },
    kiro: {
      generateIDCAuthUrl: generateKiroIDCAuthUrlMock,
    },
  },
}))

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn().mockResolvedValue([]),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

import CreateAccountModal from '../CreateAccountModal.vue'
import { DEFAULT_EXCEL_BPS_MODELS } from '@/constants/account'
import { OPENAI_EXCEL_OAUTH_CLIENT_ID } from '@/composables/useOpenAIOAuth'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

const OAuthAuthorizationFlowStub = defineComponent({
  name: 'OAuthAuthorizationFlow',
  props: {
    showManualOption: Boolean,
    showMobileRefreshTokenOption: Boolean,
    showCodexSessionImportOption: Boolean,
    showAgentIdentityOption: Boolean,
    showCodexPatOption: Boolean,
    excelOauth: Boolean,
    initialInputMethod: String,
  },
  data: () => ({ inputMethod: 'manual' }),
  methods: { reset() {} },
  emits: ['generate-url', 'import-codex-session', 'import-codex-pat'],
  template: `
    <div>
      <button data-testid="generate-url" @click="$emit('generate-url')">generate</button>
      <button data-testid="import-codex-session" @click="$emit('import-codex-session', 'session-json')">session</button>
      <button data-testid="import-codex-pat" @click="$emit('import-codex-pat', 'pat-token')">pat</button>
    </div>
  `,
})

const SelectStub = defineComponent({
  name: 'SelectStub',
  props: {
    modelValue: {
      type: [String, Number, Boolean, null],
      default: ''
    },
    options: {
      type: Array,
      default: () => []
    }
  },
  emits: ['update:modelValue', 'change'],
  template: `
    <select
      v-bind="$attrs"
      :value="modelValue"
      @change="$emit('update:modelValue', $event.target.value); $emit('change', $event.target.value, null)"
    >
      <option v-for="option in options" :key="option.value" :value="option.value">
        {{ option.label }}
      </option>
    </select>
  `
})

const GroupSelectorStub = defineComponent({
  name: 'GroupSelector',
  props: {
    modelValue: {
      type: Array,
      default: () => [],
    },
  },
  emits: ['update:modelValue'],
  template: `
    <button
      type="button"
      data-testid="select-pricing-groups"
      @click="$emit('update:modelValue', [1, 2])"
    >
      groups
    </button>
  `,
})

const ModelWhitelistSelectorStub = defineComponent({
  name: 'ModelWhitelistSelector',
  props: {
    modelValue: {
      type: Array,
      default: () => [],
    },
    platform: String,
    availableModels: { type: Array, default: undefined },
    syncCredentials: Object,
  },
  emits: ['update:modelValue', 'upstream-synced'],
  template: `<button
    type="button"
    data-testid="model-whitelist-selector"
    @click="$emit('update:modelValue', ['public-glm']); $emit('upstream-synced')"
  >models</button>`,
})

function mountModal(groups: any[] = []) {
  return mount(CreateAccountModal, {
    props: { show: true, proxies: [], groups },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        OAuthAuthorizationFlow: OAuthAuthorizationFlowStub,
        ConfirmDialog: true,
        Select: SelectStub,
        Icon: true,
        PlatformIcon: true,
        ProxySelector: true,
        ProxyAdBanner: true,
        GroupSelector: GroupSelectorStub,
        ModelWhitelistSelector: ModelWhitelistSelectorStub,
        QuotaLimitCard: true,
      },
    },
  })
}

async function selectButtonByText(wrapper: ReturnType<typeof mountModal>, text: string) {
  const button = wrapper.findAll('button').find((candidate) => candidate.text().includes(text))
  expect(button).toBeDefined()
  await button?.trigger('click')
}

async function submitApiKeyAccount(
  platform: 'openai' | 'anthropic' | 'kiro',
  enableLongContextBilling = false,
  disableUpstreamBillingProbe = false
) {
  const wrapper = mountModal()
  const platformLabel = {
    openai: 'OpenAI',
    anthropic: 'admin.accounts.claudeConsole',
    kiro: 'Kiro'
  }[platform]
  await selectButtonByText(wrapper, platformLabel)
  if (platform === 'openai' || platform === 'kiro') {
    await selectButtonByText(wrapper, 'API Key')
  }
  await wrapper.get('form#create-account-form input[type="text"]').setValue(`${platform} account`)
  await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
  if (enableLongContextBilling) {
    await wrapper.get('[data-testid="openai-long-context-billing-toggle"]').trigger('click')
  }
  if (disableUpstreamBillingProbe) {
    await wrapper.get('[data-testid="upstream-billing-auto-probe"]').trigger('click')
  }
  await wrapper.get('form#create-account-form').trigger('submit.prevent')
  await flushPromises()
  return wrapper
}

async function openCodexImportStep(toggleClicks = 0) {
  const wrapper = mountModal()
  await selectButtonByText(wrapper, 'OpenAI')
  for (let click = 0; click < toggleClicks; click += 1) {
    await wrapper.get('[data-testid="openai-long-context-billing-toggle"]').trigger('click')
  }
  await wrapper.get('form#create-account-form input[type="text"]').setValue('Codex import')
  await wrapper.get('form#create-account-form').trigger('submit.prevent')
  return wrapper
}

describe('CreateAccountModal OpenAI long-context billing', () => {
  beforeEach(() => {
    authIsSimpleMode.value = true
    createAccountMock.mockReset().mockResolvedValue({ id: 42, platform: 'openai', type: 'apikey' })
    probeUpstreamBillingMock.mockReset().mockResolvedValue({})
    syncUpstreamModelsMock.mockReset().mockResolvedValue({ models: [], metadata: {} })
    showWarningMock.mockReset()
    importCodexSessionMock.mockReset().mockResolvedValue({
      created: 1,
      updated: 0,
      skipped: 0,
      failed: 0,
      errors: [],
      warnings: [],
    })
    createOpenAICodexPATMock.mockReset().mockResolvedValue({})
    generateKiroIDCAuthUrlMock.mockReset().mockResolvedValue({
      auth_url: 'https://kiro.example/auth',
      session_id: 'kiro-session',
      state: 'kiro-state',
    })
  })

  afterEach(() => vi.useRealTimers())

  it('renders all 16 platforms in one grid with Kiro, Leonardo and Fish last', () => {
    const wrapper = mountModal()
    const platformSelector = wrapper.get('[data-tour="account-form-platform"]')

    // 手机 3 列，sm 起 4 列（16 个平台正好 4 行）。
    expect(platformSelector.classes()).toEqual(expect.arrayContaining(['grid-cols-3', 'sm:grid-cols-4']))
    expect(platformSelector.classes()).not.toContain('sm:grid-cols-5')
    expect(platformSelector.findAll('button').map((button) => button.text())).toEqual([
      'Anthropic',
      'OpenAI',
      'Gemini',
      'Antigravity',
      'Grok',
      'TypeSafe / Jev',
      'Kimi',
      'Zhipu GLM',
      'DeepSeek',
      'MiniMax',
      'OpenCode',
      'Command Code',
      'Cline',
      'Kiro',
      'Leonardo',
      'Fish Audio',
    ])
  })

  it('sets month and year expiry presets without submitting the account form', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-01-31T12:34:00'))
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('expiry account')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
    const input = wrapper.get<HTMLInputElement>('input[type="datetime-local"]')

    for (const [label, expected] of [
      ['payment.oneMonth', '2026-02-28T12:34'],
      ['payment.oneYear', '2027-01-31T12:34'],
    ]) {
      const button = wrapper.findAll('button').find((candidate) => candidate.text() === label)!
      expect(button.attributes('type')).toBe('button')
      await button.trigger('click')
      expect(input.element.value).toBe(expected)
      expect(createAccountMock).not.toHaveBeenCalled()
    }

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(createAccountMock.mock.calls[0]?.[0]?.expires_at).toBe(new Date('2027-01-31T12:34:00').getTime() / 1000)
    wrapper.unmount()
  })

  it('allows a manually entered expiry to override a preset before account creation', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('custom expiry account')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
    await selectButtonByText(wrapper, 'payment.oneMonth')
    await wrapper.get('input[type="datetime-local"]').setValue('2030-04-15T09:20')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(createAccountMock.mock.calls[0]?.[0]?.expires_at).toBe(new Date('2030-04-15T09:20:00').getTime() / 1000)
    wrapper.unmount()
  })

  it('hides only the redundant account toggle when every selected group enables tier pricing', async () => {
    authIsSimpleMode.value = false
    const wrapper = mountModal([
      { id: 1, long_context_pricing_enabled: true },
      { id: 2, long_context_pricing_enabled: true },
    ])

    await selectButtonByText(wrapper, 'OpenAI')
    await wrapper.get('[data-testid="select-pricing-groups"]').trigger('click')

    expect(wrapper.find('[data-testid="openai-long-context-billing-toggle"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="create-openai-ws-mode"]').exists()).toBe(true)
  })

  it('keeps the account toggle when any selected group disables tier pricing', async () => {
    authIsSimpleMode.value = false
    const wrapper = mountModal([
      { id: 1, long_context_pricing_enabled: true },
      { id: 2, long_context_pricing_enabled: false },
    ])

    await selectButtonByText(wrapper, 'OpenAI')
    await wrapper.get('[data-testid="select-pricing-groups"]').trigger('click')

    expect(wrapper.find('[data-testid="openai-long-context-billing-toggle"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="create-openai-ws-mode"]').exists()).toBe(true)
  })

  it('sends false explicitly for normal OpenAI account creation by default', async () => {
    await submitApiKeyAccount('openai')

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBe(false)
  })

  it('omits the upstream request id header from extra when left empty', async () => {
    await submitApiKeyAccount('openai')

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.extra).not.toHaveProperty('upstream_request_id_header')
  })

  it('sends the trimmed upstream request id header in extra when filled', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('openai account')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
    await wrapper.get('[data-testid="upstream-request-id-header"]').setValue('  X-Oneapi-Request-Id  ')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.extra?.upstream_request_id_header).toBe('X-Oneapi-Request-Id')
  })

  it('omits images_url_to_b64_json from extra by default', async () => {
    await submitApiKeyAccount('openai')

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.extra).not.toHaveProperty('images_url_to_b64_json')
  })

  it('sends images_url_to_b64_json in extra when the toggle is enabled', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('openai account')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
    await wrapper.get('[data-testid="openai-images-url-to-b64-json-toggle"]').trigger('click')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.extra?.images_url_to_b64_json).toBe(true)
  })

  it('omits image size tiers by default and sends restricted tiers when set', async () => {
    await submitApiKeyAccount('openai')
    expect(createAccountMock.mock.calls[0]?.[0]?.extra).not.toHaveProperty('openai_image_size_tiers')

    createAccountMock.mockClear()
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('openai account')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
    await wrapper.get('[data-testid="openai-image-size-tier-1K"]').setValue(false)
    await wrapper.get('[data-testid="openai-image-size-tier-2K"]').setValue(false)
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.extra?.openai_image_size_tiers).toEqual(['4K'])
  })

  it('persists upstream model metadata after creating an account from preview', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('OpenCode account')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
    await wrapper.get('[data-testid="model-whitelist-selector"]').trigger('click')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledOnce()
    expect(syncUpstreamModelsMock).toHaveBeenCalledWith(42)
  })

  it('includes the current concrete model mapping in preview credentials', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
    await wrapper.get('[data-testid="model-whitelist-selector"]').trigger('click')
    await flushPromises()

    expect(wrapper.getComponent(ModelWhitelistSelectorStub).props('syncCredentials')).toMatchObject({
      model_mapping: { 'public-glm': 'public-glm' }
    })
  })

  it('runs formal capability sync after creating an account with explicit mappings', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('Mapped account')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
    await selectButtonByText(wrapper, 'admin.accounts.modelMapping')
    await selectButtonByText(wrapper, 'admin.accounts.addMapping')
    await wrapper.get('input[placeholder="admin.accounts.requestModel"]').setValue('public-glm')
    await wrapper.get('input[placeholder="admin.accounts.actualModel"]').setValue('glm-5.3')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock.mock.calls[0]?.[0]?.credentials?.model_mapping).toEqual({
      'public-glm': 'glm-5.3'
    })
    expect(syncUpstreamModelsMock).toHaveBeenCalledWith(42)
  })

  it('warns when post-create capability metadata remains incomplete', async () => {
    syncUpstreamModelsMock.mockResolvedValue({
      models: ['x-preview-f-free'],
      warnings: [{ code: 'upstream_model_metadata_incomplete', message: 'metadata incomplete' }],
    })
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('OpenCode account')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
    await wrapper.get('[data-testid="model-whitelist-selector"]').trigger('click')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(showWarningMock).toHaveBeenCalledWith(
      'admin.accounts.syncUpstreamModelsMetadataIncomplete'
    )
  })

  // namespace 摊平是仅 OAuth 的兼容开关：API Key 走 chat completions 回退桥时由桥自行摊平
  it('shows the Codex namespace flatten toggle only for OpenAI OAuth accounts', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')

    expect(wrapper.find('[data-testid="create-openai-flatten-namespaces-toggle"]').exists()).toBe(
      true
    )

    await selectButtonByText(wrapper, 'API Key')
    expect(wrapper.find('[data-testid="create-openai-flatten-namespaces-toggle"]').exists()).toBe(
      false
    )
  })

  it('enables upstream billing probes by default for new OpenAI API key accounts', async () => {
    await submitApiKeyAccount('openai')

    expect(createAccountMock.mock.calls[0]?.[0]?.upstream_billing_probe_enabled).toBe(true)
  })

  it('waits for the initial upstream billing probe before refreshing the account list', async () => {
    let resolveProbe: (() => void) | undefined
    probeUpstreamBillingMock.mockImplementationOnce(
      () => new Promise<void>((resolve) => {
        resolveProbe = resolve
      })
    )

    const wrapper = await submitApiKeyAccount('openai')

    expect(probeUpstreamBillingMock).toHaveBeenCalledWith(42)
    expect(wrapper.emitted('created')).toBeUndefined()

    resolveProbe?.()
    await flushPromises()

    expect(wrapper.emitted('created')).toHaveLength(1)
  })

  it('sends an explicit disabled state when the create toggle is turned off', async () => {
    await submitApiKeyAccount('openai', false, true)

    expect(createAccountMock.mock.calls[0]?.[0]?.upstream_billing_probe_enabled).toBe(false)
    expect(probeUpstreamBillingMock).not.toHaveBeenCalled()
  })

  it('submits OpenCode Zen default protocol rules with adaptive endpoints', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenCode')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('oc')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('sk-opencode-zen')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      account_mode: 'zen',
      api_protocol: 'adaptive',
      base_url: 'https://opencode.ai/zen/v1',
      api_base_urls: {
        chat_completions: 'https://opencode.ai/zen/v1',
        anthropic: 'https://opencode.ai/zen',
        responses: 'https://opencode.ai/zen/v1'
      },
      protocol_rules: [
        { pattern: 'grok-*', protocol: 'responses' },
        { pattern: 'gpt-*', protocol: 'responses' },
        { pattern: 'muse-spark-*', protocol: 'responses' },
        { pattern: 'claude-*', protocol: 'anthropic' },
        { pattern: 'qwen3.8-max', protocol: 'chat_completions' },
        { pattern: 'qwen*', protocol: 'anthropic' }
      ]
    })
  })

  it('submits OpenCode GO endpoints after switching account type', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenCode')
    await selectButtonByText(wrapper, 'admin.accounts.opencodeGo.accountMode.go')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('oc-go')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('sk-opencode-go')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      account_mode: 'go',
      api_protocol: 'adaptive',
      base_url: 'https://opencode.ai/zen/go/v1',
      api_base_urls: {
        chat_completions: 'https://opencode.ai/zen/go/v1',
        anthropic: 'https://opencode.ai/zen/go',
        responses: 'https://opencode.ai/zen/go/v1'
      },
      protocol_rules: [
        { pattern: 'grok-*', protocol: 'responses' },
        { pattern: 'gpt-*', protocol: 'responses' },
        { pattern: 'muse-spark-*', protocol: 'responses' },
        { pattern: 'minimax-*', protocol: 'anthropic' },
        { pattern: 'qwen*', protocol: 'anthropic' }
      ]
    })
  })

  describe('providers using the generic form', () => {
    const serverOnlyProviders = {
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
            modes: [
              {
                mode: 'standard',
                base_urls: {
                  chat_completions: 'https://api.acme-router.example/provider/v1',
                  anthropic: 'https://api.acme-router.example/provider',
                },
                protocol_rules: [{ pattern: 'claude-*', protocol: 'anthropic' }],
              },
              {
                mode: 'team',
                base_urls: {
                  chat_completions: 'https://team.acme-router.example/provider/v1',
                  anthropic: 'https://team.acme-router.example/provider',
                },
                protocol_rules: [{ pattern: 'sonnet-*', protocol: 'anthropic' }],
              },
            ],
          },
        },
        {
          id: 'acme_chat',
          display_name: 'Acme Chat',
          gateway: 'openai',
          cn_provider: false,
          multi_protocol: {
            default_mode: 'pass',
            routing: 'by_inbound',
            modes: [{ mode: 'pass', base_urls: { chat_completions: 'https://api.acme-chat.example/v1' } }],
          },
        },
      ],
      composite_precedence: [...BUILTIN_PLATFORM_CATALOG.composite_precedence, 'acme_router', 'acme_chat'],
    }

    beforeEach(() => {
      setPlatformCatalog(serverOnlyProviders)
    })

    afterEach(() => {
      resetPlatformCatalog()
    })

    it('creates a by-model provider account from its profile defaults', async () => {
      const wrapper = mountModal()
      await wrapper.get('[data-testid="platform-button-acme_router"]').trigger('click')
      await wrapper.get('form#create-account-form input[type="text"]').setValue('cc')
      await wrapper.get('form#create-account-form input[type="password"]').setValue('sk-cc')

      await wrapper.get('form#create-account-form').trigger('submit.prevent')
      await flushPromises()

      expect(createAccountMock).toHaveBeenCalledTimes(1)
      const payload = createAccountMock.mock.calls[0]?.[0]
      expect(payload?.platform).toBe('acme_router')
      expect(payload?.type).toBe('apikey')
      expect(payload?.credentials).toMatchObject({
        api_key: 'sk-cc',
        account_mode: 'standard',
        api_protocol: 'adaptive',
        base_url: 'https://api.acme-router.example/provider/v1',
        api_base_urls: {
          chat_completions: 'https://api.acme-router.example/provider/v1',
          anthropic: 'https://api.acme-router.example/provider',
        },
        protocol_rules: [{ pattern: 'claude-*', protocol: 'anthropic' }],
      })
      // 该供应商没有原生 Responses 端点，不下发 responses 基址。
      expect(payload?.credentials?.api_base_urls).not.toHaveProperty('responses')
      // 没有内置模型列表时不预填白名单，新账号不限制模型。
      expect(payload?.credentials).not.toHaveProperty('model_mapping')
    })

    it('switches endpoints and default rules with the provider mode', async () => {
      const wrapper = mountModal()
      await wrapper.get('[data-testid="platform-button-acme_router"]').trigger('click')
      await wrapper.get('[data-testid="generic-account-mode"]').findAll('button')[1].trigger('click')
      await wrapper.get('form#create-account-form input[type="text"]').setValue('cc-team')
      await wrapper.get('form#create-account-form input[type="password"]').setValue('sk-cc')

      await wrapper.get('form#create-account-form').trigger('submit.prevent')
      await flushPromises()

      expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
        account_mode: 'team',
        base_url: 'https://team.acme-router.example/provider/v1',
        api_base_urls: {
          chat_completions: 'https://team.acme-router.example/provider/v1',
          anthropic: 'https://team.acme-router.example/provider',
        },
        protocol_rules: [{ pattern: 'sonnet-*', protocol: 'anthropic' }],
      })
    })

    it('creates a by-inbound provider account without protocol rules', async () => {
      const wrapper = mountModal()
      await wrapper.get('[data-testid="platform-button-acme_chat"]').trigger('click')
      // 单一接入模式时不显示模式选择。
      expect(wrapper.find('[data-testid="generic-account-mode"]').exists()).toBe(false)
      await wrapper.get('form#create-account-form input[type="text"]').setValue('acme-chat')
      await wrapper.get('form#create-account-form input[type="password"]').setValue('sk-acme-chat')

      await wrapper.get('form#create-account-form').trigger('submit.prevent')
      await flushPromises()

      const credentials = createAccountMock.mock.calls[0]?.[0]?.credentials
      expect(credentials).toMatchObject({
        account_mode: 'pass',
        api_protocol: 'adaptive',
        base_url: 'https://api.acme-chat.example/v1',
        api_base_urls: { chat_completions: 'https://api.acme-chat.example/v1' },
      })
      expect(credentials).not.toHaveProperty('protocol_rules')
    })

    it('falls back to the Kimi default mode after a server-only provider', async () => {
      const wrapper = mountModal()
      await wrapper.get('[data-testid="platform-button-acme_router"]').trigger('click')
      await selectButtonByText(wrapper, 'Kimi')
      await wrapper.get('form#create-account-form input[type="text"]').setValue('kimi')
      await wrapper.get('form#create-account-form input[type="password"]').setValue('sk-kimi')

      await wrapper.get('form#create-account-form').trigger('submit.prevent')
      await flushPromises()

      expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
        account_mode: 'payg',
        base_url: 'https://api.moonshot.cn/v1',
      })
      expect(createAccountMock.mock.calls[0]?.[0]?.credentials).not.toHaveProperty('protocol_rules')
    })
  })

  it('keeps Kiro and Leonardo on the last row after the generic aggregators', () => {
    const wrapper = mountModal()
    const labels = wrapper
      .get('[data-tour="account-form-platform"]')
      .findAll('button')
      .map((button) => button.text().trim())
    expect(labels.slice(-6)).toEqual(['OpenCode', 'Command Code', 'Cline', 'Kiro', 'Leonardo', 'Fish Audio'])
    expect(labels.slice(5, 10)).toEqual(['TypeSafe / Jev', 'Kimi', 'Zhipu GLM', 'DeepSeek', 'MiniMax'])
  })

  it('creates a Cline account without an account type and with only the Chat Completions endpoint', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="platform-button-cline"]').trigger('click')
    // 积分与 ClinePass 共用同一个 Key，按模型计费，不需要选择账号类型。
    expect(wrapper.find('[data-testid="generic-account-mode"]').exists()).toBe(false)
    await wrapper.get('form#create-account-form input[type="text"]').setValue('cline')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('sk-cline')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload?.platform).toBe('cline')
    expect(payload?.credentials).toMatchObject({
      account_mode: 'payg',
      api_protocol: 'adaptive',
      base_url: 'https://api.cline.bot/api/v1',
      api_base_urls: { chat_completions: 'https://api.cline.bot/api/v1' },
    })
    expect(payload?.credentials).not.toHaveProperty('protocol_rules')
    expect(payload?.credentials?.api_base_urls).not.toHaveProperty('responses')
    expect(payload?.credentials?.api_base_urls).not.toHaveProperty('anthropic')
  })

  it('submits adaptive Kimi protocol endpoints', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Kimi')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('Kimi adaptive')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('sk-kimi')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      account_mode: 'payg',
      api_protocol: 'adaptive',
      base_url: 'https://api.moonshot.cn/v1',
      api_base_urls: {
        chat_completions: 'https://api.moonshot.cn/v1',
        anthropic: 'https://api.moonshot.cn/anthropic',
        responses: 'https://api.moonshot.cn/v1'
      }
    })
  })

  it('submits adaptive Kimi Coding Plan Responses endpoint', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Kimi')
    await selectButtonByText(wrapper, 'admin.accounts.cnProviders.accountMode.coding')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('Kimi coding')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('sk-kimi-coding')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      account_mode: 'coding',
      api_protocol: 'adaptive',
      base_url: 'https://api.kimi.com/coding/v1',
      api_base_urls: {
        chat_completions: 'https://api.kimi.com/coding/v1',
        anthropic: 'https://api.kimi.com/coding',
        responses: 'https://api.kimi.com/coding/v1'
      }
    })
  })

  it('submits adaptive MiniMax protocol endpoints', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'MiniMax')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('MiniMax adaptive')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('sk-minimax')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      account_mode: 'payg',
      api_protocol: 'adaptive',
      base_url: 'https://api.minimaxi.com/v1',
      api_base_urls: {
        chat_completions: 'https://api.minimaxi.com/v1',
        anthropic: 'https://api.minimaxi.com/anthropic',
        responses: 'https://api.minimaxi.com/v1'
      }
    })
  })

  it('uses the edited adaptive Chat endpoint when previewing upstream models', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Kimi')
    await wrapper
      .get('[data-testid="cn-adaptive-base-url-chat_completions"]')
      .setValue('https://relay.example.com/v1')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('sk-relay')

    expect(wrapper.getComponent(ModelWhitelistSelectorStub).props('syncCredentials')).toMatchObject({
      platform: 'kimi',
      type: 'apikey',
      base_url: 'https://relay.example.com/v1',
      api_key: 'sk-relay'
    })
  })

  it('exposes Agent Identity in the OpenAI authorization methods', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('OpenAI account')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')

    const flow = wrapper.getComponent(OAuthAuthorizationFlowStub)
    expect(flow.props('showManualOption')).toBe(true)
    expect(flow.props('showCodexSessionImportOption')).toBe(true)
    expect(flow.props('showAgentIdentityOption')).toBe(true)
    expect(flow.props('showCodexPatOption')).toBe(true)
    expect(flow.props('initialInputMethod')).toBe('manual')
  })

  it.each([
    ['camelCase', { authMode: 'agentIdentity', agentIdentity: { agentRuntimeId: 'runtime' } }],
    ['nested identity without auth_mode', { agent_identity: { agent_runtime_id: 'runtime' } }],
  ])('accepts backend-compatible %s Agent Identity imports', async (_name, content) => {
    const wrapper = await openCodexImportStep()
    const flow = wrapper.getComponent(OAuthAuthorizationFlowStub)
    flow.vm.inputMethod = 'agent_identity'

    flow.vm.$emit('import-codex-session', JSON.stringify(content))
    await flushPromises()

    expect(importCodexSessionMock).toHaveBeenCalledTimes(1)
  })

  it('sends true explicitly when OpenAI long-context billing is enabled', async () => {
    await submitApiKeyAccount('openai', true)

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBe(true)
  })

  it('omits the OpenAI setting for non-OpenAI account creation', async () => {
    await submitApiKeyAccount('anthropic')

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBeUndefined()
    // 上游倍率探测已放宽到全部 API-key 平台：非 OpenAI 平台与 OpenAI 一致，默认开启。
    expect(createAccountMock.mock.calls[0]?.[0]?.upstream_billing_probe_enabled).toBe(true)
  })

  it('sends an explicit disabled state when the non-OpenAI create toggle is turned off', async () => {
    await submitApiKeyAccount('anthropic', false, true)

    expect(createAccountMock.mock.calls[0]?.[0]?.upstream_billing_probe_enabled).toBe(false)
  })

  it('creates a Kiro direct API-key account with upstream billing probe disabled by default', async () => {
    await submitApiKeyAccount('kiro')

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload?.platform).toBe('kiro')
    expect(payload?.type).toBe('apikey')
    expect(payload?.credentials?.api_key).toBe('test-api-key')
    expect(payload?.credentials?.api_region).toBe('us-east-1')
    expect(payload?.upstream_billing_probe_enabled).toBe(false)
    expect(probeUpstreamBillingMock).not.toHaveBeenCalled()
  })

  it('creates a Kiro external relay API-key account with upstream billing probe enabled by default', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Kiro')
    await selectButtonByText(wrapper, 'API Key + Base URL')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('kiro relay account')
    const baseUrlInput = wrapper
      .findAll('input')
      .find((candidate) => candidate.attributes('placeholder') === 'https://your-relay.example.com')
    expect(baseUrlInput).toBeDefined()
    await baseUrlInput?.setValue('https://relay.example')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload?.platform).toBe('kiro')
    expect(payload?.type).toBe('apikey')
    expect(payload?.credentials?.base_url).toBe('https://relay.example')
    expect(payload?.upstream_billing_probe_enabled).toBe(true)
    expect(probeUpstreamBillingMock).toHaveBeenCalledWith(42)
  })

  it('creates a Kiro API-key account with the selected API region', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Kiro')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('kiro eu account')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('ksk-eu')
    const regionSelect = wrapper.get<HTMLSelectElement>('[data-testid="kiro-api-region-select"]')
    expect(regionSelect.element.value).toBe('us-east-1')
    expect(regionSelect.find('option[value="eu-central-1"]').exists()).toBe(true)
    expect(regionSelect.find('option[value="eu-central-1"]').text()).toBe('eu-central-1')
    await regionSelect.setValue('eu-central-1')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      api_key: 'ksk-eu',
      api_region: 'eu-central-1'
    })
  })

  it('uses the Kiro region select for IDC authorization URLs', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Kiro')
    await selectButtonByText(wrapper, 'admin.accounts.oauth.kiro.idcTitle')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('kiro idc account')

    const regionSelect = wrapper.get<HTMLSelectElement>('[data-testid="kiro-idc-region-select"]')
    expect(regionSelect.element.value).toBe('us-east-1')
    expect(regionSelect.find('option[value="eu-central-1"]').exists()).toBe(true)
    expect(regionSelect.find('option[value="eu-central-1"]').text()).toBe('eu-central-1')

    await regionSelect.setValue('eu-central-1')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()
    await wrapper.get('[data-testid="generate-url"]').trigger('click')
    await flushPromises()

    expect(generateKiroIDCAuthUrlMock).toHaveBeenCalledWith({
      proxy_id: undefined,
      start_url: 'https://view.awsapps.com/start',
      region: 'eu-central-1'
    })
  })

  it('antigravity upstream 创建默认携带上游倍率探测开关', async () => {
    // antigravity upstream 走独立创建 helper，
    // 也必须与其余 API-key 平台一样默认开启探测并传递开关。
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Antigravity')
    await selectButtonByText(wrapper, 'admin.accounts.types.antigravityApikey')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('antigravity relay')
    const baseInput = wrapper
      .findAll('input')
      .find((candidate) => candidate.attributes('placeholder') === 'https://cloudcode-pa.googleapis.com')
    expect(baseInput).toBeDefined()
    await baseInput?.setValue('https://relay.example')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('sk-upstream')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload?.platform).toBe('antigravity')
    expect(payload?.type).toBe('apikey')
    expect(payload?.upstream_billing_probe_enabled).toBe(true)
    // 创建成功后前端立即发起一次首探（与其他 apikey 平台一致）。
    expect(probeUpstreamBillingMock).toHaveBeenCalledWith(42)
  })

  it('OpenAI 的 OAuth / BPS OAuth / API Key 三个类型按钮在同一行', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')

    const grid = wrapper.get('[data-testid="openai-account-type-grid"]')
    expect(grid.classes()).toContain('grid-cols-3')
    expect(grid.classes()).not.toContain('grid-cols-2')
    const labels = grid.findAll(':scope > button').map((button) => button.find('span.block').text())
    expect(labels).toEqual(['OAuth', 'BPS OAuth', 'API Key'])
  })

  it('BPS OAuth 只保留 Excel 可用的授权入口', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await wrapper.get('[data-testid="openai-bps-oauth"]').trigger('click')
    expect(wrapper.find('[data-testid="bps-oauth-models"]').exists()).toBe(false)
    await wrapper.get('form#create-account-form input[type="text"]').setValue('BPS account')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    const flow = wrapper.findComponent({ name: 'OAuthAuthorizationFlow' })
    expect(flow.exists()).toBe(true)
    expect((wrapper.vm as any).form.type).toBe('bps-oauth')
    expect(flow.props('showMobileRefreshTokenOption')).toBe(false)
    expect(flow.props('showCodexSessionImportOption')).toBe(false)
    expect(flow.props('showAgentIdentityOption')).toBe(false)
    expect(flow.props('showCodexPatOption')).toBe(false)
  })

  it('BPS OAuth 第二步提示 Excel 回调，返回上一步保留 BPS 类型与模型', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await wrapper.get('[data-testid="openai-bps-oauth"]').trigger('click')
    const models = [...(wrapper.vm as any).allowedModels]
    await wrapper.get('form#create-account-form input[type="text"]').setValue('BPS account')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(wrapper.findComponent({ name: 'OAuthAuthorizationFlow' }).props('excelOauth')).toBe(true)

    ;(wrapper.vm as any).goBackToBasicInfo()
    await flushPromises()

    expect((wrapper.vm as any).form.type).toBe('bps-oauth')
    expect((wrapper.vm as any).openaiBPSOAuth).toBe(true)
    expect((wrapper.vm as any).allowedModels).toEqual(models)
  })

  it('BPS OAuth 默认在通用模型限制里预填 BPS 模型，候选只列 BPS 模型', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    const section = () => wrapper.get('[data-testid="oauth-model-restriction-section"]')
    // 普通 OAuth：原生目录，不限定候选
    expect(section().getComponent(ModelWhitelistSelectorStub).props('availableModels')).toBeUndefined()
    expect(section().getComponent(ModelWhitelistSelectorStub).props('modelValue')).toContain('gpt-image-2')

    await wrapper.get('[data-testid="openai-bps-oauth"]').trigger('click')
    await flushPromises()
    const selector = section().getComponent(ModelWhitelistSelectorStub)
    expect(selector.props('modelValue')).toEqual([...DEFAULT_EXCEL_BPS_MODELS])
    expect(selector.props('availableModels')).toEqual([...DEFAULT_EXCEL_BPS_MODELS])

    const mappingTab = section().findAll('button').find((button) => button.text() === 'admin.accounts.modelMapping')
    await mappingTab!.trigger('click')
    expect(section().findAll('[data-testid="oauth-preset-mapping"]').map((button) => button.text())).toEqual([
      '+ GPT-6 Astra',
      '+ GPT-5.6 Sol',
      '+ GPT-5.6 Terra',
    ])

    // 切回普通 OAuth：恢复原生预设与白名单
    await selectButtonByText(wrapper, 'admin.accounts.types.chatgptOauth')
    await flushPromises()
    expect(section().findAll('[data-testid="oauth-preset-mapping"]').map((button) => button.text())).toContain('+ GPT-4o')
    const whitelistTab = section().findAll('button').find((button) => button.text() === 'admin.accounts.modelWhitelist')
    await whitelistTab!.trigger('click')
    await flushPromises()
    expect(section().getComponent(ModelWhitelistSelectorStub).props('availableModels')).toBeUndefined()
    expect(section().getComponent(ModelWhitelistSelectorStub).props('modelValue')).toContain('gpt-image-2')
  })

  it('BPS OAuth 建号把模型写进 model_mapping，不再写 openai_excel_bps_models', async () => {
    generateAuthUrlMock.mockResolvedValue({
      auth_url: 'https://auth.openai.com/oauth/authorize?client_id=excel&state=bps.test',
      session_id: 'session-1',
    })
    exchangeCodeMock.mockResolvedValue({
      access_token: 'at',
      refresh_token: 'rt',
      client_id: OPENAI_EXCEL_OAUTH_CLIENT_ID,
    })
    createAccountMock.mockResolvedValue({ id: 42 })

    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await wrapper.get('[data-testid="openai-bps-oauth"]').trigger('click')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('BPS account')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()
    await wrapper.get('[data-testid="generate-url"]').trigger('click')
    await flushPromises()
    expect(generateAuthUrlMock).toHaveBeenCalledWith(expect.any(String), expect.objectContaining({ oauth_client: 'excel' }))

    await (wrapper.vm as any).handleOpenAIExchange('auth-code')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload?.type).toBe('bps-oauth')
    expect(payload?.credentials?.model_mapping).toEqual({
      'gpt-6-astra': 'gpt-6-astra',
      'gpt-5.6-sol': 'gpt-5.6-sol',
      'gpt-5.6-terra': 'gpt-5.6-terra',
    })
    expect(payload?.extra ?? {}).not.toHaveProperty('openai_excel_bps_models')
  })

  it('BPS OAuth 不受 Codex 透传开关影响，始终应用模型映射', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    ;(wrapper.vm as any).openaiPassthroughEnabled = true
    await flushPromises()
    expect((wrapper.vm as any).isOpenAIModelRestrictionDisabled).toBe(true)

    await wrapper.get('[data-testid="openai-bps-oauth"]').trigger('click')
    await flushPromises()
    expect((wrapper.vm as any).isOpenAIModelRestrictionDisabled).toBe(false)
  })

  it('普通 OpenAI OAuth 仍提供 Codex 导入入口', async () => {
    const wrapper = await openCodexImportStep()
    const flow = wrapper.findComponent({ name: 'OAuthAuthorizationFlow' })
    expect((wrapper.vm as any).form.type).toBe('oauth')
    expect(flow.props('showMobileRefreshTokenOption')).toBe(true)
    expect(flow.props('showCodexSessionImportOption')).toBe(true)
    expect(flow.props('showAgentIdentityOption')).toBe(true)
    expect(flow.props('showCodexPatOption')).toBe(true)
  })

  it('leaves Codex session import billing ownership to the backend', async () => {
    const wrapper = await openCodexImportStep()
    await wrapper.get('[data-testid="import-codex-session"]').trigger('click')
    await flushPromises()

    expect(importCodexSessionMock).toHaveBeenCalledTimes(1)
    expect(importCodexSessionMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBeUndefined()
  })

  it('leaves Codex PAT import billing ownership to the backend', async () => {
    const wrapper = await openCodexImportStep()
    await wrapper.get('[data-testid="import-codex-pat"]').trigger('click')
    await flushPromises()

    expect(createOpenAICodexPATMock).toHaveBeenCalledTimes(1)
    expect(createOpenAICodexPATMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBeUndefined()
  })

  it('sends explicit true for Codex session import after the toggle is enabled', async () => {
    const wrapper = await openCodexImportStep(1)
    await wrapper.get('[data-testid="import-codex-session"]').trigger('click')
    await flushPromises()

    expect(importCodexSessionMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBe(true)
  })

  it('sends explicit false for Codex session import after the toggle is changed back', async () => {
    const wrapper = await openCodexImportStep(2)
    await wrapper.get('[data-testid="import-codex-session"]').trigger('click')
    await flushPromises()

    expect(importCodexSessionMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBe(false)
  })

  it('sends explicit true for Codex PAT import after the toggle is enabled', async () => {
    const wrapper = await openCodexImportStep(1)
    await wrapper.get('[data-testid="import-codex-pat"]').trigger('click')
    await flushPromises()

    expect(createOpenAICodexPATMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBe(true)
  })

  it('sends explicit false for Codex PAT import after the toggle is changed back', async () => {
    const wrapper = await openCodexImportStep(2)
    await wrapper.get('[data-testid="import-codex-pat"]').trigger('click')
    await flushPromises()

    expect(createOpenAICodexPATMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBe(false)
  })

  it('shows the Telemetry toggle only for OpenAI OAuth accounts', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    expect(wrapper.find('[data-testid="create-codex-telemetry-toggle"]').exists()).toBe(true)

    await selectButtonByText(wrapper, 'API Key')
    expect(wrapper.find('[data-testid="create-codex-telemetry-toggle"]').exists()).toBe(false)
  })

  it('omits codex_telemetry_enabled from extra by default', async () => {
    const wrapper = await openCodexImportStep()
    await wrapper.get('[data-testid="import-codex-pat"]').trigger('click')
    await flushPromises()

    expect(createOpenAICodexPATMock.mock.calls[0]?.[0]?.extra).not.toHaveProperty('codex_telemetry_enabled')
  })

  it('submits extra.codex_telemetry_enabled when Telemetry is enabled for OpenAI OAuth', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await wrapper.get('[data-testid="create-codex-telemetry-toggle"]').trigger('click')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('Codex import')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await wrapper.get('[data-testid="import-codex-pat"]').trigger('click')
    await flushPromises()

    expect(createOpenAICodexPATMock).toHaveBeenCalledTimes(1)
    expect(createOpenAICodexPATMock.mock.calls[0]?.[0]?.extra?.codex_telemetry_enabled).toBe(true)
    expect(JSON.stringify(createOpenAICodexPATMock.mock.calls[0]?.[0])).not.toMatch(/抗降智/)
  })

  it('allows enabling the Kiro direct API-key upstream billing probe', async () => {
    await submitApiKeyAccount('kiro', false, true)

    expect(createAccountMock.mock.calls[0]?.[0]?.upstream_billing_probe_enabled).toBe(true)
    expect(probeUpstreamBillingMock).toHaveBeenCalledWith(42)
  })
})


describe('CreateAccountModal gemini explicit cache upstream switch', () => {
  beforeEach(() => {
    authIsSimpleMode.value = true
    createAccountMock.mockReset().mockResolvedValue({ id: 43, platform: 'gemini', type: 'apikey' })
    probeUpstreamBillingMock.mockReset().mockResolvedValue({})
    syncUpstreamModelsMock.mockReset().mockResolvedValue({ models: [], metadata: {} })
  })

  it('only offers the switch for a custom base url and sends it on create', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Gemini')
    await selectButtonByText(wrapper, 'admin.accounts.gemini.accountType.apiKeyTitle')
    expect(wrapper.find('[data-testid="explicit-cache-upstream-section"]').exists()).toBe(false)

    const baseUrlInput = wrapper
      .findAll('form#create-account-form input[type="text"]')
      .find((input) => (input.element as HTMLInputElement).value === 'https://generativelanguage.googleapis.com')
    expect(baseUrlInput).toBeDefined()
    await baseUrlInput!.setValue('https://upstream.example.com')
    expect(wrapper.find('[data-testid="explicit-cache-upstream-section"]').exists()).toBe(true)

    await wrapper.get('[data-testid="explicit-cache-upstream-toggle"]').trigger('click')
    await wrapper.get('[data-testid="explicit-cache-upstream-max-ttl"]').setValue('1800')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('gemini upstream gateway')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('sk-upstream')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      base_url: 'https://upstream.example.com',
      explicit_cache_upstream: true,
      explicit_cache_upstream_max_ttl_seconds: 1800
    })
  })

  it('only offers the AI Studio tier for the official base url', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Gemini')
    await selectButtonByText(wrapper, 'admin.accounts.gemini.accountType.apiKeyTitle')
    expect(wrapper.find('[data-testid="gemini-oauth-tier"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="gemini-apikey-note"]').exists()).toBe(true)
    await wrapper.get('[data-testid="gemini-apikey-tier"]').setValue('aistudio_paid')

    const baseUrlInput = wrapper
      .findAll('form#create-account-form input[type="text"]')
      .find((input) => (input.element as HTMLInputElement).value === 'https://generativelanguage.googleapis.com')
    expect(baseUrlInput).toBeDefined()
    await baseUrlInput!.setValue('https://upstream.example.com')
    expect(wrapper.find('[data-testid="gemini-apikey-tier"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="gemini-apikey-note"]').exists()).toBe(false)

    await wrapper.get('form#create-account-form input[type="text"]').setValue('gemini relay')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('sk-upstream')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const credentials = createAccountMock.mock.calls[0]?.[0]?.credentials
    expect(credentials).toMatchObject({ base_url: 'https://upstream.example.com' })
    expect(credentials).not.toHaveProperty('tier_id')
  })

  it('shows the OAuth tier fallback only on the OAuth form', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Gemini')
    await selectButtonByText(wrapper, 'admin.accounts.gemini.accountType.oauthTitle')
    expect(wrapper.find('[data-testid="gemini-oauth-tier"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="gemini-apikey-note"]').exists()).toBe(false)
  })

  it('keeps the selected AI Studio tier for the official base url', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Gemini')
    await selectButtonByText(wrapper, 'admin.accounts.gemini.accountType.apiKeyTitle')
    await wrapper.get('[data-testid="gemini-apikey-tier"]').setValue('aistudio_paid')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('gemini official')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('AIza-official')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      base_url: 'https://generativelanguage.googleapis.com',
      tier_id: 'aistudio_paid'
    })
  })
})

describe('CreateAccountModal Vertex API key auth', () => {
  beforeEach(() => {
    authIsSimpleMode.value = true
    createAccountMock.mockReset().mockResolvedValue({ id: 44, platform: 'gemini', type: 'service_account' })
    probeUpstreamBillingMock.mockReset().mockResolvedValue({})
    syncUpstreamModelsMock.mockReset().mockResolvedValue({ models: [], metadata: {} })
  })

  async function openGeminiVertexApiKeyForm() {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Gemini')
    await selectButtonByText(wrapper, 'admin.accounts.vertexLabel')
    await wrapper.get('[data-testid="vertex-auth-mode-apikey"]').setValue(true)
    await wrapper.get('form#create-account-form input[type="text"]').setValue('vertex api key account')
    await wrapper.get('[data-testid="vertex-api-key-input"]').setValue(' vertex-key ')
    return wrapper
  }

  it('creates a Gemini Vertex account with API key auth and no project for the express endpoint', async () => {
    const wrapper = await openGeminiVertexApiKeyForm()
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload?.platform).toBe('gemini')
    expect(payload?.type).toBe('service_account')
    expect(payload?.credentials).toEqual({
      auth_mode: 'apikey',
      api_key: 'vertex-key',
      location: 'global',
      tier_id: 'vertex'
    })
  })

  it('sends the project id when an API key account targets a project endpoint', async () => {
    const wrapper = await openGeminiVertexApiKeyForm()
    await wrapper.get('[data-testid="vertex-api-key-project-id-input"]').setValue(' my-project ')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      auth_mode: 'apikey',
      api_key: 'vertex-key',
      project_id: 'my-project',
      location: 'global'
    })
  })

  it('keeps Anthropic Vertex accounts on Service Account JSON only', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'admin.accounts.vertexLabel')
    expect(wrapper.find('[data-testid="vertex-auth-mode-apikey"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="vertex-api-key-input"]').exists()).toBe(false)
  })
})

describe('CreateAccountModal Leonardo accounts', () => {
  beforeEach(() => {
    authIsSimpleMode.value = true
    createAccountMock.mockReset().mockResolvedValue({ id: 7, platform: 'leonardo', type: 'oauth' })
  })

  it('moves the cookie account to the credential step instead of asking for an API key', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="platform-tab-leonardo"]').trigger('click')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('Leonardo cookie')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="leonardo-cookie-input"]').setValue('__Secure-better-auth.session_token=abc')
    await wrapper.get('[data-testid="leonardo-create-account"]').trigger('click')
    await flushPromises()

    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload?.platform).toBe('leonardo')
    expect(payload?.type).toBe('oauth')
    expect(payload?.credentials).toMatchObject({ cookie: '__Secure-better-auth.session_token=abc' })
  })

  it('sends the Leonardo cookie credit unit price in extra', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="platform-tab-leonardo"]').trigger('click')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('Leonardo cookie')
    await wrapper.get('[data-testid="leonardo-credit-unit-price-usd"]').setValue('0.0012')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    await wrapper.get('[data-testid="leonardo-cookie-input"]').setValue('__Secure-better-auth.session_token=abc')
    await wrapper.get('[data-testid="leonardo-create-account"]').trigger('click')
    await flushPromises()

    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload?.type).toBe('oauth')
    expect(payload?.extra?.leonardo_credit_unit_price_usd).toBe(0.0012)
    // 未设出图上限：写出中性值（不限 / 默认 2 倍）。
    expect(payload?.extra).toMatchObject({
      leonardo_max_quality: '',
      leonardo_max_short_edge: 0,
      leonardo_max_upscale_factor: 2
    })
  })

  it('sends the Leonardo cookie image limits in extra', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="platform-tab-leonardo"]').trigger('click')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('Leonardo cookie')
    await wrapper.get('[data-testid="leonardo-max-short-edge"]').setValue('1024')
    await wrapper.get('[data-testid="leonardo-max-upscale-factor"]').setValue('1.5')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    await wrapper.get('[data-testid="leonardo-cookie-input"]').setValue('__Secure-better-auth.session_token=abc')
    await wrapper.get('[data-testid="leonardo-create-account"]').trigger('click')
    await flushPromises()

    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload?.extra).toMatchObject({ leonardo_max_short_edge: 1024, leonardo_max_upscale_factor: 1.5 })
  })

  it('creates the relay account in one step with base_url and api_key', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="platform-tab-leonardo"]').trigger('click')
    await wrapper.get('[data-testid="leonardo-account-type-relay"]').trigger('click')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('Leonardo relay')
    await wrapper.get('[data-testid="leonardo-relay-base-url"]').setValue('https://relay.example.com')
    await wrapper.get('[data-testid="leonardo-relay-api-key"]').setValue('sk-relay')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload?.platform).toBe('leonardo')
    expect(payload?.type).toBe('apikey')
    expect(payload?.credentials).toMatchObject({ base_url: 'https://relay.example.com', api_key: 'sk-relay' })
    expect(wrapper.find('[data-testid="leonardo-credit-unit-price-usd"]').exists()).toBe(false)
    expect(payload?.extra?.leonardo_credit_unit_price_usd).toBeUndefined()
    expect(payload?.extra?.leonardo_max_short_edge).toBeUndefined()
  })
})

describe('CreateAccountModal Fish accounts', () => {
  beforeEach(() => {
    authIsSimpleMode.value = true
    createAccountMock.mockReset().mockResolvedValue({ id: 8, platform: 'fish', type: 'apikey' })
  })

  it('shows both account types on the first step and creates a relay as apikey', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="platform-tab-fish"]').trigger('click')

    expect(wrapper.find('[data-testid="fish-account-type-token"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="fish-account-type-relay"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="fish-relay-base-url"]').exists()).toBe(false)

    await wrapper.get('[data-testid="fish-account-type-relay"]').trigger('click')
    expect(wrapper.find('[data-testid="fish-relay-base-url"]').exists()).toBe(true)
    await wrapper.get('form#create-account-form input[type="text"]').setValue('Fish relay')
    await wrapper.get('[data-testid="fish-relay-base-url"]').setValue('https://relay.example.com')
    await wrapper.get('[data-testid="fish-relay-api-key"]').setValue('sk-relay')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload?.platform).toBe('fish')
    expect(payload?.type).toBe('apikey')
    expect(payload?.credentials).toMatchObject({ base_url: 'https://relay.example.com', api_key: 'sk-relay' })
    expect(wrapper.find('[data-testid="fish-model-restriction-section"]').exists()).toBe(true)
  })

  it('creates an oauth token account on the first step', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="platform-tab-fish"]').trigger('click')

    expect(wrapper.find('[data-testid="fish-token-input"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="fish-model-restriction-section"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('admin.accounts.oauth.authMethod')

    await wrapper.get('[data-tour="account-form-name"]').setValue('Fish token')
    await wrapper.get('[data-testid="fish-token-input"]').setValue('46471c2b-7cdc-4671-acc4-c0b2fd111817')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload?.platform).toBe('fish')
    expect(payload?.type).toBe('apikey')
    // 原生号 token 落在 api_key：调度快照只保留 api_key（见 044adb592）。
    expect(payload?.credentials).toEqual({ api_key: '46471c2b-7cdc-4671-acc4-c0b2fd111817' })
    expect(wrapper.find('[data-testid="fish-token-input"]').exists()).toBe(true)
  })
})
