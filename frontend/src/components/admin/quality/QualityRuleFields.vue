<template>
  <div class="quality-rule-fields space-y-5">
    <label class="block space-y-1"><span>{{ t('qualityOps.ruleName') }}</span><input v-model="form.rule_name" maxlength="100" class="input" data-testid="quality-rule-name-input" /></label>

    <slot name="account" />

    <label class="block space-y-1"><span>{{ t('qualityOps.models') }}</span><input v-model="form.models" required class="input" data-testid="quality-models" placeholder="gpt-5.5, gpt-5.4" /><span class="block text-xs text-gray-500 dark:text-gray-400">{{ t('qualityOps.modelsHint') }}</span></label>
    <div class="grid gap-4 sm:grid-cols-2">
      <div class="space-y-1"><span class="block">{{ t('qualityOps.questionKind') }}</span><Select v-model="form.question_kind" :options="questionKindOptions" data-testid="quality-question-kind" /></div>
      <div class="space-y-1"><span class="block">{{ t('qualityOps.effort') }}</span><Select v-model="form.reasoning_effort" :options="effortOptions" data-testid="quality-effort" /></div>
      <div class="space-y-1">
        <span class="flex items-center gap-1">{{ t('qualityOps.cron') }}
          <HelpTooltip>
            <template #trigger><span class="help-dot">?</span></template>
            <div class="space-y-1.5">
              <p class="font-medium">{{ t('admin.scheduledTests.cronTooltipTitle') }}</p>
              <p>{{ t('admin.scheduledTests.cronTooltipMeaning') }}</p>
              <p>{{ t('admin.scheduledTests.cronTooltipExampleEvery30Min') }}</p>
              <p>{{ t('admin.scheduledTests.cronTooltipExampleHourly') }}</p>
              <p>{{ t('admin.scheduledTests.cronTooltipExampleDaily') }}</p>
              <p>{{ t('admin.scheduledTests.cronTooltipExampleWeekly') }}</p>
              <p>{{ t('admin.scheduledTests.cronTooltipRange') }}</p>
            </div>
          </HelpTooltip>
        </span>
        <Input v-model="form.cron_expression" placeholder="*/30 * * * *" data-testid="quality-cron" :hint="t('admin.scheduledTests.cronHelp')" />
      </div>
      <label class="space-y-1"><span class="block">{{ t('qualityOps.parallel') }}</span><input v-model.number="form.parallel_count" type="number" min="1" :max="QUALITY_MAX_SAMPLES_PER_MODEL" step="1" required class="input" data-testid="quality-parallel" /></label>
    </div>
    <template v-if="form.question_kind === 'random_reasoning'">
      <p class="text-sm text-gray-500 dark:text-gray-400" data-testid="quality-random-hint">{{ t('qualityOps.randomHint') }}</p>
      <slot name="random-hint" />
    </template>
    <template v-else>
      <div><div class="mb-2 flex items-center justify-between"><label :for="promptId">{{ t('qualityOps.prompt') }}</label><button type="button" class="text-sm text-primary-600" @click="useCandy">{{ t('qualityOps.candy') }}</button></div><textarea :id="promptId" v-model="form.prompt" required maxlength="32000" rows="5" class="input text-sm" data-testid="quality-prompt" /></div>
      <label class="block space-y-1"><span>{{ t('qualityOps.answer') }}</span><input v-model="form.expected_answer" required maxlength="4000" class="input" data-testid="quality-expected-answer" /></label>
    </template>

    <fieldset class="editor-section">
      <legend>{{ t('qualityOps.judgeTitle') }}</legend>
      <div class="grid gap-4 sm:grid-cols-2">
        <div class="space-y-1"><span class="block">{{ t('qualityOps.judgeGroup') }}</span><Select v-model="form.judge.group_id" :options="judgeGroupOptions" :placeholder="t('qualityOps.selectJudgeGroup')" searchable data-testid="quality-judge-group" /></div>
        <label class="space-y-1"><span class="block">{{ t('qualityOps.judgeModel') }}</span><input v-model="form.judge.model_id" :list="judgeModelsId" required maxlength="100" class="input" data-testid="quality-judge-model" :placeholder="t('qualityOps.selectJudgeModel')" /><datalist :id="judgeModelsId"><option v-for="model in judgeModels" :key="model" :value="model" /></datalist></label>
      </div>
      <label class="block space-y-1"><span>{{ t('qualityOps.judgePrompt') }}</span><textarea v-model="form.judge.prompt" required maxlength="16000" rows="3" class="input text-sm" data-testid="quality-judge-prompt" /></label>
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('qualityOps.grading') }}</p>
    </fieldset>

    <fieldset class="editor-section">
      <legend>{{ t('qualityOps.failureAction') }}</legend>
      <label class="flex items-center gap-2"><input v-model="form.action" type="radio" value="observe_only" :disabled="scopeTaken('observe_only')" data-testid="quality-action-observe-only" />{{ t('qualityOps.observeOnly') }}<span v-if="scopeTaken('observe_only')" class="text-xs text-gray-500">{{ t('qualityOps.scopeTaken') }}</span></label>
      <label class="flex items-center gap-2"><input v-model="form.action" type="radio" value="disable_scheduling" :disabled="scopeTaken('disable_scheduling')" data-testid="quality-action-disable-scheduling" />{{ t('qualityOps.disableScheduling') }}<span v-if="scopeTaken('disable_scheduling')" class="text-xs text-gray-500">{{ t('qualityOps.scopeTaken') }}</span></label>
      <label class="flex items-center gap-2"><input v-model="form.action" type="radio" value="remove_groups" :disabled="scopeTaken('remove_groups')" data-testid="quality-action-remove-groups" />{{ t('qualityOps.removeGroups') }}<span v-if="scopeTaken('remove_groups')" class="text-xs text-gray-500">{{ t('qualityOps.scopeTaken') }}</span></label>
      <GroupSelector v-if="qualityUsesGroups(form.action)" v-model="form.remove_group_ids" class="pl-6" :groups="groups" :label="t('qualityOps.targetGroups')" data-testid="quality-target-groups" />
      <label v-if="qualityUsesRetestRest(form.action)" class="block space-y-1" data-testid="quality-retest-rest-setting"><span>{{ t('qualityOps.retestRest') }}</span><input v-model.number="form.retest_rest_minutes" type="number" min="0" max="10080" step="1" required class="input" data-testid="quality-retest-rest" /><span class="block text-xs text-gray-500 dark:text-gray-400">{{ t('qualityOps.retestRestHint') }}</span></label>
      <template v-if="form.action !== 'observe_only'"><label class="flex items-center gap-2"><input v-model="form.auto_restore" type="checkbox" data-testid="quality-auto-restore" />{{ t('qualityOps.autoRestore') }}</label><p class="text-sm text-gray-500 dark:text-gray-400">{{ t('qualityOps.restoreHelp') }}</p></template>
    </fieldset>
    <label class="flex items-center gap-2"><input v-model="form.enabled" type="checkbox" data-testid="quality-rule-enabled" />{{ t('qualityOps.enabled') }}</label>
  </div>
</template>

<script setup lang="ts">
// The quality rule fields shared by the global quality page and the
// single-account rule dialog. Callers own the surrounding <form> and saving.
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupSelector from '@/components/common/GroupSelector.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Input from '@/components/common/Input.vue'
import Select from '@/components/common/Select.vue'
import { getModelAllowlistCandidates } from '@/api/admin/groups'
import { CANDY_ANSWER, CANDY_PROMPT } from '@/utils/intelligenceTest'
import {
  QUALITY_MAX_SAMPLES_PER_MODEL,
  QUALITY_REASONING_EFFORTS,
  qualityRuleScope,
  qualityUsesGroups,
  qualityUsesRetestRest,
  type QualityRuleForm,
  type QualityRuleScope
} from '@/utils/qualityRulePatch'
import type { AdminGroup, QualityAction } from '@/types'

const props = withDefaults(defineProps<{
  groups: AdminGroup[]
  // Judge model candidates load only while the editor is open.
  active?: boolean
  // Scopes already owned by another rule of the same account.
  disabledScopes?: QualityRuleScope[]
}>(), { active: true, disabledScopes: () => [] })
const emit = defineEmits<{ error: [message: string] }>()
const form = defineModel<QualityRuleForm>({ required: true })

const { t } = useI18n()
const uid = useId()
const promptId = `quality-prompt-${uid}`
const judgeModelsId = `quality-judge-models-${uid}`

const questionKindOptions = computed(() => [
  { value: 'random_reasoning', label: t('qualityOps.questionRandom') },
  { value: 'candy', label: t('qualityOps.questionCandy') }
])
const effortOptions = QUALITY_REASONING_EFFORTS.map(effort => ({ value: effort, label: effort }))
const judgeGroupOptions = computed(() => props.groups.filter(g => g.status === 'active').map(g => ({ value: g.id, label: `${g.name} #${g.id}` })))

function scopeTaken(action: QualityAction) {
  return props.disabledScopes.includes(qualityRuleScope(action))
}
function useCandy() { form.value.prompt = CANDY_PROMPT; form.value.expected_answer = CANDY_ANSWER }

const judgeModels = ref<string[]>([])
let judgeModelsRequest = 0
async function loadJudgeModels(groupId: number) {
  const request = ++judgeModelsRequest
  judgeModels.value = []
  if (!groupId) return
  try {
    const models = await getModelAllowlistCandidates(groupId)
    if (request === judgeModelsRequest) judgeModels.value = models
  } catch { if (request === judgeModelsRequest) emit('error', t('qualityOps.judgeModelsUnavailable')) }
}
watch(() => props.active ? form.value.judge.group_id : 0, id => { void loadJudgeModels(id) }, { immediate: true })
onBeforeUnmount(() => { judgeModelsRequest++ })
</script>

<style scoped>
.quality-rule-fields label, .quality-rule-fields legend, .quality-rule-fields .space-y-1 > span { @apply text-sm; }
.quality-rule-fields .input { @apply w-full; }
.editor-section { @apply space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-600; }
.editor-section legend { @apply px-2 font-medium; }
.help-dot { @apply inline-flex h-4 w-4 cursor-help items-center justify-center rounded-full border border-gray-400/70 text-[10px] font-semibold text-gray-400 transition-colors hover:border-primary-500 hover:text-primary-600 dark:border-gray-500 dark:text-gray-500; }
</style>
