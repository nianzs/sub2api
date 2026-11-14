// Quality rule helpers. qualityJudgeConfigured, qualityUsesGroups,
// qualityUsesRetestRest and qualityActionPayload are ported from
// ranxi2001/sub2api@f895ea743 (frontend/src/utils/qualityRulePatch.ts) with the
// BPS, model-isolation and OAuth 401 parts removed. The editor form helpers
// below are new.
import type {
  CreateScheduledTestPlanRequest,
  PelicanTestConfig,
  QualityAction,
  QualityJudgeConfig,
  QualityPolicy,
  QualityQuestionKind,
  ScheduledTestPlan
} from '@/types'
import { CANDY_ANSWER, CANDY_PROMPT } from './intelligenceTest'

export const QUALITY_ACTIONS: readonly QualityAction[] = ['observe_only', 'disable_scheduling', 'remove_groups']
export const QUALITY_REASONING_EFFORTS = ['minimal', 'low', 'medium', 'high', 'xhigh'] as const
export const QUALITY_MAX_SAMPLES_PER_MODEL = 8
const MAX_RETEST_REST_MINUTES = 10080

export function qualityJudgeConfigured(judge?: QualityJudgeConfig): judge is QualityJudgeConfig {
  if (!judge?.prompt.trim()) return false
  return judge.group_id > 0 && !!judge.model_id.trim()
}

export function qualityUsesGroups(action?: QualityAction) {
  return action === 'remove_groups'
}

export function qualityUsesRetestRest(action?: QualityAction) {
  return action === 'remove_groups' || action === 'disable_scheduling'
}

// The backend keeps one observation rule and one quarantine rule per account
// (unique index in migration 253).
export type QualityRuleScope = 'observation' | 'quarantine'
export function qualityRuleScope(action?: QualityAction): QualityRuleScope {
  return action === 'observe_only' ? 'observation' : 'quarantine'
}

// Action cleanup and validation shared by every save path. Errors are i18n keys.
export function qualityActionPayload(policy: QualityPolicy): QualityPolicy {
  const next: QualityPolicy = {
    ...policy,
    remove_group_ids: qualityUsesGroups(policy.action) ? [...policy.remove_group_ids] : []
  }
  const rest = qualityUsesRetestRest(next.action) ? policy.retest_rest_minutes ?? 0 : 0
  if (!Number.isInteger(rest) || rest < 0 || rest > MAX_RETEST_REST_MINUTES) throw new Error('qualityOps.invalidRetestRest')
  if (rest > 0) next.retest_rest_minutes = rest
  else delete next.retest_rest_minutes
  if (qualityUsesGroups(next.action) && !next.remove_group_ids.length) throw new Error('qualityOps.selectGroups')
  if (next.action === 'observe_only') next.auto_restore = false
  return next
}

// Flat editor state for one quality rule. `models` is the raw text the admin
// typed; parseQualityModels turns it into the ordered model list.
export interface QualityRuleForm {
  rule_name: string
  models: string
  question_kind: QualityQuestionKind
  prompt: string
  expected_answer: string
  reasoning_effort: string
  cron_expression: string
  parallel_count: number
  judge: QualityJudgeConfig
  action: QualityAction
  remove_group_ids: number[]
  auto_restore: boolean
  retest_rest_minutes: number
  enabled: boolean
  max_results: number
  auto_recover: boolean
}

export type QualityRulePayload = Omit<CreateScheduledTestPlanRequest, 'account_id'> & {
  pelican_config: PelicanTestConfig & { quality: QualityPolicy }
}

export function defaultQualityRuleForm(judgePrompt: string): QualityRuleForm {
  return {
    rule_name: '',
    models: '',
    question_kind: 'random_reasoning',
    prompt: CANDY_PROMPT,
    expected_answer: CANDY_ANSWER,
    reasoning_effort: 'high',
    cron_expression: '*/30 * * * *',
    parallel_count: 2,
    judge: { group_id: 0, model_id: '', prompt: judgePrompt },
    action: 'observe_only',
    remove_group_ids: [],
    auto_restore: true,
    retest_rest_minutes: 60,
    enabled: true,
    max_results: 100,
    auto_recover: false
  }
}

export function qualityRuleFormFromPlan(plan: ScheduledTestPlan, judgePrompt: string): QualityRuleForm {
  const base = defaultQualityRuleForm(judgePrompt)
  const cfg = plan.pelican_config
  const quality = cfg?.quality
  const action = quality && QUALITY_ACTIONS.includes(quality.action) ? quality.action : base.action
  const random = cfg?.question_kind === 'random_reasoning'
  return {
    ...base,
    rule_name: cfg?.rule_name || '',
    models: (cfg?.model_ids?.length ? cfg.model_ids : [plan.model_id]).join(', '),
    question_kind: random ? 'random_reasoning' : 'candy',
    // Random rules keep the candy defaults so switching kinds starts from a valid pair.
    prompt: random ? base.prompt : cfg?.prompt ?? '',
    expected_answer: random ? base.expected_answer : quality?.expected_answer ?? '',
    reasoning_effort: cfg?.reasoning_effort || base.reasoning_effort,
    cron_expression: plan.cron_expression,
    parallel_count: cfg?.parallel_count || base.parallel_count,
    judge: quality?.judge ? { ...quality.judge } : base.judge,
    action,
    remove_group_ids: [...(quality?.remove_group_ids || [])],
    // Observation rules always store auto_restore=false; show the default for when the action changes.
    auto_restore: action === 'observe_only' ? base.auto_restore : quality?.auto_restore ?? base.auto_restore,
    retest_rest_minutes: qualityUsesRetestRest(action) ? quality?.retest_rest_minutes ?? 0 : base.retest_rest_minutes,
    enabled: plan.enabled,
    max_results: plan.max_results,
    auto_recover: plan.auto_recover
  }
}

export function parseQualityModels(text: string): string[] {
  return [...new Set(text.split(/[\s,，]+/).map(model => model.trim()).filter(Boolean))]
}

// Build the scheduled-test-plan body shared by create (plus account_id) and update.
// The server replaces pelican_config as a whole, so every editable field is sent.
export function qualityRulePayload(form: QualityRuleForm): QualityRulePayload {
  const models = parseQualityModels(form.models)
  if (!models.length || models.some(model => model.length > 100)) throw new Error('qualityOps.modelRequired')
  const samples = form.parallel_count
  if (!Number.isInteger(samples) || samples < 1 || samples > QUALITY_MAX_SAMPLES_PER_MODEL) throw new Error('qualityOps.invalidParallel')
  if (models.length > 50 || models.length * samples > 100) throw new Error('qualityOps.tooManyModelSamples')
  const cron = form.cron_expression.trim().split(/\s+/).join(' ')
  if (cron.split(' ').length !== 5) throw new Error('qualityOps.invalidCron')

  const judge: QualityJudgeConfig = { group_id: form.judge.group_id, model_id: form.judge.model_id.trim(), prompt: form.judge.prompt }
  if (!qualityJudgeConfigured(judge)) throw new Error('qualityOps.configureJudge')
  const random = form.question_kind === 'random_reasoning'
  const prompt = random ? '' : form.prompt
  const expected = random ? '' : form.expected_answer.trim()
  if (!random && (!prompt.trim() || !expected)) throw new Error('qualityOps.questionRequired')

  const quality = qualityActionPayload({
    judge,
    expected_answer: expected,
    action: form.action,
    remove_group_ids: form.remove_group_ids,
    retest_rest_minutes: form.retest_rest_minutes,
    auto_restore: form.auto_restore
  })
  const config: QualityRulePayload['pelican_config'] = {
    question_kind: form.question_kind,
    prompt,
    reasoning_effort: form.reasoning_effort,
    parallel_count: samples,
    model_ids: models,
    quality
  }
  const ruleName = form.rule_name.trim()
  if (ruleName) config.rule_name = ruleName
  return {
    model_id: models[0],
    cron_expression: cron,
    enabled: form.enabled,
    max_results: Math.max(form.max_results, models.length * samples),
    auto_recover: form.auto_recover,
    pelican_config: config
  }
}
