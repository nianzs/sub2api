import { describe, expect, it } from 'vitest'
import type { QualityPolicy, ScheduledTestPlan } from '@/types'
import { CANDY_ANSWER, CANDY_PROMPT } from '@/utils/intelligenceTest'
import {
  defaultQualityRuleForm,
  parseQualityModels,
  qualityActionPayload,
  qualityJudgeConfigured,
  qualityRuleFormFromPlan,
  qualityRulePayload,
  type QualityRuleForm
} from '@/utils/qualityRulePatch'

const judge = { group_id: 7, model_id: 'gpt-5.5-mini', prompt: 'compare' }

function policy(overrides: Partial<QualityPolicy> = {}): QualityPolicy {
  return { judge, expected_answer: '', action: 'observe_only', remove_group_ids: [3], retest_rest_minutes: 60, auto_restore: true, ...overrides }
}

function form(overrides: Partial<QualityRuleForm> = {}): QualityRuleForm {
  return { ...defaultQualityRuleForm('compare'), models: 'gpt-5.5', judge: { ...judge }, ...overrides }
}

describe('qualityActionPayload', () => {
  it('observe_only drops groups, rest and auto-restore', () => {
    expect(qualityActionPayload(policy())).toEqual({ judge, expected_answer: '', action: 'observe_only', remove_group_ids: [], auto_restore: false })
  })

  it('disable_scheduling keeps rest and auto-restore but no groups', () => {
    expect(qualityActionPayload(policy({ action: 'disable_scheduling' }))).toEqual({
      judge, expected_answer: '', action: 'disable_scheduling', remove_group_ids: [], retest_rest_minutes: 60, auto_restore: true
    })
  })

  it('remove_groups keeps target groups and requires at least one', () => {
    expect(qualityActionPayload(policy({ action: 'remove_groups', remove_group_ids: [3, 4], auto_restore: false }))).toEqual({
      judge, expected_answer: '', action: 'remove_groups', remove_group_ids: [3, 4], retest_rest_minutes: 60, auto_restore: false
    })
    expect(() => qualityActionPayload(policy({ action: 'remove_groups', remove_group_ids: [] }))).toThrow('qualityOps.selectGroups')
  })

  it('omits a zero rest and rejects invalid rest values', () => {
    expect(qualityActionPayload(policy({ action: 'disable_scheduling', retest_rest_minutes: 0 }))).not.toHaveProperty('retest_rest_minutes')
    for (const value of [-1, 1.5, 10081, Number.NaN]) {
      expect(() => qualityActionPayload(policy({ action: 'disable_scheduling', retest_rest_minutes: value }))).toThrow('qualityOps.invalidRetestRest')
    }
  })

  it('does not mutate the source group list', () => {
    const source = policy({ action: 'remove_groups', remove_group_ids: [3] })
    qualityActionPayload(source).remove_group_ids.push(9)
    expect(source.remove_group_ids).toEqual([3])
  })
})

describe('qualityJudgeConfigured', () => {
  it('needs a group, a model and a prompt', () => {
    expect(qualityJudgeConfigured(judge)).toBe(true)
    expect(qualityJudgeConfigured({ ...judge, group_id: 0 })).toBe(false)
    expect(qualityJudgeConfigured({ ...judge, model_id: ' ' })).toBe(false)
    expect(qualityJudgeConfigured({ ...judge, prompt: '' })).toBe(false)
    expect(qualityJudgeConfigured(undefined)).toBe(false)
  })
})

describe('qualityRulePayload', () => {
  it('builds the default random-reasoning observation rule', () => {
    expect(qualityRulePayload(form())).toEqual({
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
        model_ids: ['gpt-5.5'],
        quality: { judge, expected_answer: '', action: 'observe_only', remove_group_ids: [], auto_restore: false }
      }
    })
  })

  it('sends the candy question with its expected answer and rule name', () => {
    const body = qualityRulePayload(form({ question_kind: 'candy', rule_name: '  nightly  ', action: 'disable_scheduling', retest_rest_minutes: 0 }))
    expect(body.pelican_config).toMatchObject({
      rule_name: 'nightly',
      question_kind: 'candy',
      prompt: CANDY_PROMPT,
      quality: { expected_answer: CANDY_ANSWER, action: 'disable_scheduling', auto_restore: true }
    })
    expect(body.pelican_config.quality).not.toHaveProperty('retest_rest_minutes')
  })

  it('dedupes models, normalizes cron and raises retention to cover a round', () => {
    const body = qualityRulePayload(form({ models: 'a, b，a\nc', cron_expression: ' 0  *  * * * ', parallel_count: 8, max_results: 10 }))
    expect(body.model_id).toBe('a')
    expect(body.pelican_config.model_ids).toEqual(['a', 'b', 'c'])
    expect(body.cron_expression).toBe('0 * * * *')
    expect(body.max_results).toBe(24)
  })

  it('rejects incomplete or invalid drafts', () => {
    expect(() => qualityRulePayload(form({ models: ' , ' }))).toThrow('qualityOps.modelRequired')
    expect(() => qualityRulePayload(form({ parallel_count: 9 }))).toThrow('qualityOps.invalidParallel')
    expect(() => qualityRulePayload(form({ parallel_count: 0 }))).toThrow('qualityOps.invalidParallel')
    expect(() => qualityRulePayload(form({ models: Array.from({ length: 51 }, (_, i) => `m${i}`).join(',') , parallel_count: 1 }))).toThrow('qualityOps.tooManyModelSamples')
    expect(() => qualityRulePayload(form({ cron_expression: '*/30 * * *' }))).toThrow('qualityOps.invalidCron')
    expect(() => qualityRulePayload(form({ judge: { ...judge, group_id: 0 } }))).toThrow('qualityOps.configureJudge')
    expect(() => qualityRulePayload(form({ question_kind: 'candy', expected_answer: ' ' }))).toThrow('qualityOps.questionRequired')
    expect(() => qualityRulePayload(form({ action: 'remove_groups', remove_group_ids: [] }))).toThrow('qualityOps.selectGroups')
  })
})

describe('qualityRuleFormFromPlan', () => {
  const plan: ScheduledTestPlan = {
    id: 5, account_id: 9, model_id: 'gpt-5.5', cron_expression: '0 * * * *', enabled: false, max_results: 50, auto_recover: true,
    last_run_at: null, next_run_at: null, created_at: '', updated_at: '',
    pelican_config: {
      rule_name: 'r', question_kind: 'candy', prompt: 'q?', reasoning_effort: 'low', parallel_count: 3, model_ids: ['gpt-5.5', 'gpt-5.4'],
      quality: { judge, expected_answer: '42', action: 'disable_scheduling', remove_group_ids: [], auto_restore: false }
    }
  }

  it('round-trips a saved rule through the editor', () => {
    const draft = qualityRuleFormFromPlan(plan, 'default prompt')
    expect(draft).toMatchObject({ models: 'gpt-5.5, gpt-5.4', prompt: 'q?', expected_answer: '42', retest_rest_minutes: 0, auto_restore: false, enabled: false })
    const body = qualityRulePayload(draft)
    expect(body).toEqual({
      model_id: 'gpt-5.5', cron_expression: '0 * * * *', enabled: false, max_results: 50, auto_recover: true,
      pelican_config: { rule_name: 'r', question_kind: 'candy', prompt: 'q?', reasoning_effort: 'low', parallel_count: 3, model_ids: ['gpt-5.5', 'gpt-5.4'],
        quality: { judge, expected_answer: '42', action: 'disable_scheduling', remove_group_ids: [], auto_restore: false } }
    })
  })

  it('shows defaults for fields an observation rule does not store', () => {
    const observe: ScheduledTestPlan = { ...plan, pelican_config: { ...plan.pelican_config!, question_kind: 'random_reasoning', prompt: '', quality: { ...plan.pelican_config!.quality!, action: 'observe_only' } } }
    expect(qualityRuleFormFromPlan(observe, 'p')).toMatchObject({ question_kind: 'random_reasoning', prompt: CANDY_PROMPT, auto_restore: true, retest_rest_minutes: 60 })
  })

  it('parses models in order without duplicates', () => {
    expect(parseQualityModels(' x  y,x ')).toEqual(['x', 'y'])
  })
})
