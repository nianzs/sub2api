import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, type PropType } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import type { ScheduledTestPlan } from '@/types'

const api = vi.hoisted(() => ({
  listByAccount: vi.fn(),
  listResults: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  deletePlan: vi.fn(),
  runQualityPlan: vi.fn(),
  listQualityPlans: vi.fn(),
  getAllIncludingInactive: vi.fn(),
  getModelAllowlistCandidates: vi.fn()
}))

vi.mock('@/api/admin/scheduledTests', () => ({
  default: { listByAccount: api.listByAccount, listResults: api.listResults, create: api.create, update: api.update, delete: api.deletePlan }
}))
vi.mock('@/api/admin/accountQuality', () => ({ runQualityPlan: api.runQualityPlan, listQualityPlans: api.listQualityPlans, default: {} }))
vi.mock('@/api/admin/groups', () => {
  const groups = { getAllIncludingInactive: api.getAllIncludingInactive, getModelAllowlistCandidates: api.getModelAllowlistCandidates }
  return { ...groups, default: groups }
})
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key, te: (key: string) => key.startsWith('qualityOps.') }) }
})

import AccountQualitySection from '../AccountQualitySection.vue'

const BaseDialogStub = defineComponent({
  props: { show: Boolean, title: String },
  setup(props, { slots }) {
    return () => props.show ? h('section', { 'data-dialog': props.title }, [slots.default?.(), slots.footer?.()]) : null
  }
})
type Option = { value: string | number; label: string }
const SelectStub = defineComponent({
  props: { modelValue: [String, Number, Boolean], options: { type: Array as PropType<Option[]>, default: () => [] } },
  emits: ['update:modelValue', 'change'],
  setup(props, { emit }) {
    return () => h('select', {
      value: props.modelValue,
      onChange: (event: Event) => {
        const raw = (event.target as HTMLSelectElement).value
        const value = props.options.find(option => String(option.value) === raw)?.value ?? raw
        emit('update:modelValue', value)
        emit('change', value)
      }
    }, props.options.map(option => h('option', { value: option.value }, option.label)))
  }
})
const InputStub = defineComponent({
  props: { modelValue: String },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    return () => h('input', { value: props.modelValue, onInput: (event: Event) => emit('update:modelValue', (event.target as HTMLInputElement).value) })
  }
})

const judge = { group_id: 7, model_id: 'gpt-5.5-mini', prompt: 'compare' }
function qualityPlan(id: number, action: 'observe_only' | 'disable_scheduling', extra: Partial<ScheduledTestPlan> = {}): ScheduledTestPlan {
  return {
    id, account_id: 101, account_name: 'acc-a', model_id: 'gpt-5.5', cron_expression: '0 * * * *', enabled: true, max_results: 100,
    auto_recover: false, last_run_at: null, next_run_at: null, created_at: '', updated_at: '',
    pelican_config: {
      question_kind: 'random_reasoning', prompt: '', reasoning_effort: 'high', parallel_count: 2, model_ids: ['gpt-5.5'],
      quality: { judge, expected_answer: '', action, remove_group_ids: [], auto_restore: action !== 'observe_only' }
    },
    ...extra
  }
}
const plainPlan: ScheduledTestPlan = {
  id: 5, account_id: 101, model_id: 'gpt-5.5', cron_expression: '*/5 * * * *', enabled: true, max_results: 50,
  auto_recover: false, last_run_at: null, next_run_at: null, created_at: '', updated_at: ''
}

let wrapper: VueWrapper | undefined
const submitted = vi.fn()

// Mounted inside a form, like the account editor, to catch accidental submits.
function mountSection() {
  const Host = defineComponent({
    setup() {
      return () => h('form', { onSubmit: (event: Event) => { event.preventDefault(); submitted() } }, [
        h(AccountQualitySection, { account: { id: 101, name: 'acc-a' } })
      ])
    }
  })
  wrapper = mount(Host, {
    attachTo: document.body,
    global: { stubs: { BaseDialog: BaseDialogStub, Select: SelectStub, Input: InputStub, GroupSelector: true, HelpTooltip: true } }
  })
  return wrapper
}
const byTestId = (view: VueWrapper, id: string) => view.find(`[data-testid="${id}"]`)

describe('AccountQualitySection', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.listResults.mockResolvedValue([])
    api.listQualityPlans.mockResolvedValue([])
    api.getAllIncludingInactive.mockResolvedValue([{ id: 7, name: 'judges', status: 'active' }])
    api.getModelAllowlistCandidates.mockResolvedValue(['gpt-5.5-mini'])
  })
  afterEach(() => { wrapper?.unmount(); wrapper = undefined })

  it('lists only this account\'s quality rules with their latest result', async () => {
    api.listByAccount.mockResolvedValue([qualityPlan(11, 'observe_only'), plainPlan])
    api.listResults.mockResolvedValue([{ id: 1, plan_id: 11, status: 'success', quality_action: 'passed', started_at: '2026-11-14T10:00:00Z' }])
    const view = mountSection()
    await flushPromises()

    expect(api.listByAccount).toHaveBeenCalledWith(101)
    const rules = view.findAll('[data-testid="account-quality-rule"]')
    expect(rules).toHaveLength(1)
    expect(rules[0].attributes('data-plan-id')).toBe('11')
    expect(byTestId(view, 'account-quality-last-result').text()).toContain('qualityOps.outcomes.passed')
    expect(byTestId(view, 'account-quality-history').attributes('href')).toBe('/admin/account-quality?plan=11')
  })

  it('creates a rule for this account only, without an account picker', async () => {
    api.listByAccount.mockResolvedValue([])
    api.create.mockResolvedValue(qualityPlan(12, 'observe_only'))
    const view = mountSection()
    await flushPromises()
    expect(byTestId(view, 'account-quality-empty').exists()).toBe(true)

    await byTestId(view, 'account-quality-add').trigger('click')
    await flushPromises()
    expect(byTestId(view, 'account-quality-account').text()).toContain('acc-a')
    expect(byTestId(view, 'quality-account-picker').exists()).toBe(false)

    await byTestId(view, 'quality-models').setValue('gpt-5.5')
    await byTestId(view, 'quality-judge-group').setValue('7')
    await byTestId(view, 'quality-judge-model').setValue('gpt-5.5-mini')
    await byTestId(view, 'account-quality-form').trigger('submit')
    await flushPromises()

    expect(api.create).toHaveBeenCalledTimes(1)
    const body = api.create.mock.calls[0][0]
    expect(body.account_id).toBe(101)
    expect(body.pelican_config.model_ids).toEqual(['gpt-5.5'])
    expect(body.pelican_config.quality).toMatchObject({ action: 'observe_only', judge: { group_id: 7, model_id: 'gpt-5.5-mini' } })
    expect(submitted).not.toHaveBeenCalled()
  })

  it('blocks a second rule of the same scope and starts a new rule in the free scope', async () => {
    api.listByAccount.mockResolvedValue([qualityPlan(11, 'observe_only')])
    const view = mountSection()
    await flushPromises()

    await byTestId(view, 'account-quality-add').trigger('click')
    await flushPromises()
    const observe = byTestId(view, 'quality-action-observe-only').element as HTMLInputElement
    expect(observe.disabled).toBe(true)
    expect((byTestId(view, 'quality-action-disable-scheduling').element as HTMLInputElement).checked).toBe(true)
  })

  it('hides the add button once both scopes are used', async () => {
    api.listByAccount.mockResolvedValue([qualityPlan(11, 'observe_only'), qualityPlan(12, 'disable_scheduling')])
    const view = mountSection()
    await flushPromises()
    expect(view.findAll('[data-testid="account-quality-rule"]')).toHaveLength(2)
    expect(byTestId(view, 'account-quality-add').exists()).toBe(false)
  })

  it('toggles and runs a rule without submitting the account form', async () => {
    api.listByAccount.mockResolvedValue([qualityPlan(11, 'observe_only')])
    api.update.mockResolvedValue(qualityPlan(11, 'observe_only', { enabled: false }))
    api.runQualityPlan.mockResolvedValue(undefined)
    const view = mountSection()
    await flushPromises()

    await byTestId(view, 'account-quality-run').trigger('click')
    await flushPromises()
    expect(api.runQualityPlan).toHaveBeenCalledWith(11)

    await byTestId(view, 'account-quality-toggle').trigger('click')
    await flushPromises()
    expect(api.update).toHaveBeenCalledWith(11, { enabled: false })
    expect(submitted).not.toHaveBeenCalled()
  })

  it('copies test settings from another account\'s rule but keeps this account\'s action', async () => {
    api.listByAccount.mockResolvedValue([])
    const source = { ...qualityPlan(40, 'disable_scheduling'), account_id: 202, account_name: 'acc-b', cron_expression: '15 * * * *' }
    source.pelican_config!.reasoning_effort = 'xhigh'
    api.listQualityPlans.mockResolvedValue([source])
    api.create.mockResolvedValue(qualityPlan(12, 'observe_only'))
    const view = mountSection()
    await flushPromises()

    await byTestId(view, 'account-quality-add').trigger('click')
    await flushPromises()
    await byTestId(view, 'account-quality-copy').find('select').setValue('40')
    await byTestId(view, 'account-quality-form').trigger('submit')
    await flushPromises()

    const body = api.create.mock.calls[0][0]
    expect(body.account_id).toBe(101)
    expect(body.cron_expression).toBe('15 * * * *')
    expect(body.pelican_config.reasoning_effort).toBe('xhigh')
    expect(body.pelican_config.quality.action).toBe('observe_only')
  })
})
