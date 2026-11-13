import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, type PropType } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import type { ScheduledTestPlan } from '@/types'

const api = vi.hoisted(() => ({
  listQualityPlans: vi.fn(),
  listQualityOperations: vi.fn(),
  runQualityPlan: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  deletePlan: vi.fn(),
  listResults: vi.fn(),
  getResult: vi.fn(),
  listAccounts: vi.fn(),
  getAllIncludingInactive: vi.fn(),
  getModelAllowlistCandidates: vi.fn()
}))

vi.mock('@/api/admin/accountQuality', () => ({
  listQualityPlans: api.listQualityPlans,
  listQualityOperations: api.listQualityOperations,
  runQualityPlan: api.runQualityPlan,
  default: {}
}))
vi.mock('@/api/admin/scheduledTests', () => ({
  default: { create: api.create, update: api.update, delete: api.deletePlan, listResults: api.listResults, getResult: api.getResult }
}))
vi.mock('@/api/admin/accounts', () => ({ list: api.listAccounts, default: { list: api.listAccounts } }))
vi.mock('@/api/admin/groups', () => {
  const groups = { getAllIncludingInactive: api.getAllIncludingInactive, getModelAllowlistCandidates: api.getModelAllowlistCandidates }
  return { ...groups, default: groups }
})
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key, te: (key: string) => key.startsWith('qualityOps.') }) }
})

import AccountQualityView from '../AccountQualityView.vue'

const SlotStub = defineComponent({ setup(_, { slots }) { return () => h('div', slots.default?.()) } })
const BaseDialogStub = defineComponent({
  props: { show: Boolean, title: String },
  setup(props, { slots }) {
    return () => props.show ? h('section', { 'data-dialog': props.title }, [slots.default?.(), slots.footer?.()]) : null
  }
})
type Option = { value: string | number; label: string }
const SelectStub = defineComponent({
  props: { modelValue: [String, Number, Boolean], options: { type: Array as PropType<Option[]>, default: () => [] } },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    return () => h('select', {
      value: props.modelValue,
      onChange: (event: Event) => {
        const raw = (event.target as HTMLSelectElement).value
        emit('update:modelValue', props.options.find(option => String(option.value) === raw)?.value ?? raw)
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
const GroupSelectorStub = defineComponent({
  props: { modelValue: { type: Array as PropType<number[]>, default: () => [] }, groups: { type: Array as PropType<{ id: number }[]>, default: () => [] } },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    return () => h('div', props.groups.map(group => h('input', {
      type: 'checkbox',
      'data-group-id': group.id,
      checked: props.modelValue.includes(group.id),
      onChange: (event: Event) => emit('update:modelValue', (event.target as HTMLInputElement).checked
        ? [...props.modelValue, group.id]
        : props.modelValue.filter(id => id !== group.id))
    })))
  }
})

const judge = { group_id: 7, model_id: 'gpt-5.5-mini', prompt: 'compare' }
const observationPlan: ScheduledTestPlan = {
  id: 11, account_id: 101, account_name: 'acc-a', model_id: 'gpt-5.5', cron_expression: '0 * * * *', enabled: true, max_results: 100,
  auto_recover: false, last_run_at: null, next_run_at: null, created_at: '', updated_at: '',
  pelican_config: {
    question_kind: 'random_reasoning', prompt: '', reasoning_effort: 'high', parallel_count: 2, model_ids: ['gpt-5.5'],
    quality: { judge, expected_answer: '', action: 'observe_only', remove_group_ids: [], auto_restore: false }
  }
}

let wrapper: VueWrapper | undefined

function mountView() {
  wrapper = mount(AccountQualityView, {
    global: {
      stubs: {
        AppLayout: SlotStub,
        BaseDialog: BaseDialogStub,
        Select: SelectStub,
        Input: InputStub,
        GroupSelector: GroupSelectorStub,
        HelpTooltip: true,
        Icon: true,
        RouterLink: true
      }
    }
  })
  return wrapper
}

function field(view: VueWrapper, testId: string) {
  return view.find(`[data-testid="${testId}"]`)
}

async function fillJudge(view: VueWrapper) {
  await field(view, 'quality-judge-group').setValue('7')
  await field(view, 'quality-judge-model').setValue('gpt-5.5-mini')
}

describe('AccountQualityView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.listQualityPlans.mockResolvedValue([observationPlan])
    api.listQualityOperations.mockResolvedValue({ items: [], next_cursor: 0 })
    api.getAllIncludingInactive.mockResolvedValue([
      { id: 7, name: 'judges', status: 'active' },
      { id: 8, name: 'pool', status: 'active' }
    ])
    api.getModelAllowlistCandidates.mockResolvedValue(['gpt-5.5-mini'])
    api.listAccounts.mockResolvedValue({
      items: [{ id: 101, name: 'acc-a' }, { id: 102, name: 'acc-b' }, { id: 103, name: 'acc-c' }],
      total: 3
    })
    api.create.mockImplementation(async (body: { account_id: number }) => ({ id: body.account_id + 1000 }))
    api.update.mockResolvedValue({})
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
  })

  it('creates one remove-groups rule per selected account', async () => {
    const view = mountView()
    await flushPromises()
    await field(view, 'quality-create-rule').trigger('click')
    await flushPromises()

    expect(api.listAccounts).toHaveBeenCalledWith(1, 50, expect.objectContaining({ platform: 'openai' }))
    // acc-a already has an observation rule, so it cannot get another one.
    expect(view.find('[data-account-id="101"] input').attributes('disabled')).toBeDefined()

    await field(view, 'quality-action-remove-groups').setValue(true)
    expect(view.find('[data-account-id="101"] input').attributes('disabled')).toBeUndefined()
    await view.find('[data-account-id="102"] input').setValue(true)
    await view.find('[data-account-id="103"] input').setValue(true)
    await field(view, 'quality-models').setValue('gpt-5.5, gpt-5.4 gpt-5.5')
    await fillJudge(view)
    await field(view, 'quality-judge-prompt').setValue('compare')
    await view.find('[data-group-id="8"]').setValue(true)
    expect(api.getModelAllowlistCandidates).toHaveBeenCalledWith(7)

    await view.find('form#quality-rule-form').trigger('submit')
    await flushPromises()

    const body = {
      model_id: 'gpt-5.5',
      cron_expression: '*/30 * * * *',
      enabled: true,
      max_results: 100,
      auto_recover: false,
      pelican_config: {
        question_kind: 'random_reasoning',
        prompt: '',
        reasoning_effort: 'high',
        parallel_count: 2,
        model_ids: ['gpt-5.5', 'gpt-5.4'],
        quality: { judge, expected_answer: '', action: 'remove_groups', remove_group_ids: [8], retest_rest_minutes: 60, auto_restore: true }
      }
    }
    expect(api.create).toHaveBeenCalledTimes(2)
    expect(api.create).toHaveBeenNthCalledWith(1, { ...body, account_id: 102 })
    expect(api.create).toHaveBeenNthCalledWith(2, { ...body, account_id: 103 })
    expect(view.find('form#quality-rule-form').exists()).toBe(false)
    expect(api.listQualityPlans).toHaveBeenCalledTimes(2)
  })

  it('sends an observe-only candy rule without groups, rest or auto-restore', async () => {
    const view = mountView()
    await flushPromises()
    await field(view, 'quality-create-rule').trigger('click')
    await flushPromises()

    await view.find('[data-account-id="102"] input').setValue(true)
    await field(view, 'quality-models').setValue('gpt-5.5')
    await field(view, 'quality-question-kind').setValue('candy')
    await field(view, 'quality-expected-answer').setValue('21')
    await field(view, 'quality-parallel').setValue('3')
    await fillJudge(view)
    await field(view, 'quality-judge-prompt').setValue('compare')
    await view.find('form#quality-rule-form').trigger('submit')
    await flushPromises()

    expect(api.create).toHaveBeenCalledTimes(1)
    const sent = api.create.mock.calls[0][0]
    expect(sent.account_id).toBe(102)
    expect(sent.pelican_config.question_kind).toBe('candy')
    expect(sent.pelican_config.prompt).toContain('糖果')
    expect(sent.pelican_config.parallel_count).toBe(3)
    expect(sent.pelican_config.quality).toEqual({ judge, expected_answer: '21', action: 'observe_only', remove_group_ids: [], auto_restore: false })
  })

  it('updates an existing rule in place', async () => {
    const view = mountView()
    await flushPromises()
    await view.find('[data-plan-id="11"] [data-testid="quality-rule-edit"]').trigger('click')
    await flushPromises()

    expect(field(view, 'quality-account-picker').exists()).toBe(false)
    await field(view, 'quality-action-disable-scheduling').setValue(true)
    await field(view, 'quality-retest-rest').setValue('30')
    await view.find('form#quality-rule-form').trigger('submit')
    await flushPromises()

    expect(api.create).not.toHaveBeenCalled()
    expect(api.update).toHaveBeenCalledWith(11, {
      model_id: 'gpt-5.5',
      cron_expression: '0 * * * *',
      enabled: true,
      max_results: 100,
      auto_recover: false,
      pelican_config: {
        question_kind: 'random_reasoning',
        prompt: '',
        reasoning_effort: 'high',
        parallel_count: 2,
        model_ids: ['gpt-5.5'],
        quality: { judge, expected_answer: '', action: 'disable_scheduling', remove_group_ids: [], retest_rest_minutes: 30, auto_restore: true }
      }
    })
  })

  it('blocks saving without a grading model and keeps the editor open', async () => {
    const view = mountView()
    await flushPromises()
    await field(view, 'quality-create-rule').trigger('click')
    await flushPromises()

    await view.find('[data-account-id="102"] input').setValue(true)
    await field(view, 'quality-models').setValue('gpt-5.5')
    await view.find('form#quality-rule-form').trigger('submit')
    await flushPromises()

    expect(api.create).not.toHaveBeenCalled()
    expect(field(view, 'quality-form-error').text()).toBe('qualityOps.configureJudge')
  })

  it('keeps only the accounts that failed to save selected', async () => {
    api.create.mockResolvedValueOnce({ id: 1 }).mockRejectedValueOnce({ message: 'account busy' })
    const view = mountView()
    await flushPromises()
    await field(view, 'quality-create-rule').trigger('click')
    await flushPromises()

    await view.find('[data-account-id="102"] input').setValue(true)
    await view.find('[data-account-id="103"] input').setValue(true)
    await field(view, 'quality-models').setValue('gpt-5.5')
    await fillJudge(view)
    await field(view, 'quality-judge-prompt').setValue('compare')
    await view.find('form#quality-rule-form').trigger('submit')
    await flushPromises()

    expect(api.create).toHaveBeenCalledTimes(2)
    expect(field(view, 'quality-form-error').text()).toBe('account busy')
    expect((view.find('[data-account-id="102"] input').element as HTMLInputElement).checked).toBe(false)
    expect((view.find('[data-account-id="103"] input').element as HTMLInputElement).checked).toBe(true)
  })
})
