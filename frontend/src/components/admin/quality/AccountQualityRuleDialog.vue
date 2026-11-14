<template>
  <BaseDialog :show="show" :title="plan ? t('qualityOps.edit') : t('qualityOps.create')" width="wide" :z-index="60" :close-on-escape="!busy && !confirmDelete && !discardPrompt" @close="close">
    <p v-if="formError" role="alert" class="editor-error" data-testid="account-quality-form-error">{{ formError }}</p>
    <p v-if="groupsError" role="alert" class="editor-error">{{ groupsError }} <button type="button" class="underline" @click="loadGroups">{{ t('qualityOps.retry') }}</button></p>
    <form :id="formId" class="quality-editor" data-testid="account-quality-form" @submit.prevent.stop="save"><fieldset :disabled="busy" class="space-y-5">
      <QualityRuleFields v-model="form" :groups="groups" :active="show" :disabled-scopes="takenScopes" @error="formError = $event">
        <template #account>
          <div class="editor-account" data-testid="account-quality-account"><span>{{ t('qualityOps.accounts') }}</span><strong>{{ accountName || `#${accountId}` }}</strong><span>#{{ accountId }}</span></div>
          <div v-if="copySources.length" class="space-y-1" data-testid="account-quality-copy">
            <span class="block">{{ t('qualityOps.accountSection.copyFrom') }}</span>
            <Select v-model="copySourceId" :options="copySourceOptions" :placeholder="t('qualityOps.accountSection.copyFromPlaceholder')" searchable @change="copyFromSource" />
            <span class="block text-xs text-gray-500 dark:text-gray-400">{{ t('qualityOps.accountSection.copyFromHint') }}</span>
          </div>
        </template>
        <template #random-hint>
          <p class="rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-950/30 dark:text-amber-300" data-testid="account-quality-random-peer-hint">{{ t('qualityOps.accountSection.randomPeerHint') }}</p>
        </template>
      </QualityRuleFields>
    </fieldset></form>
    <template #footer><div class="editor-footer"><button v-if="plan" type="button" class="delete-rule" data-testid="account-quality-delete" :disabled="busy" @click="confirmDelete = true">{{ t('qualityOps.delete') }}</button><span class="flex-1" /><button type="button" class="btn btn-secondary" :disabled="busy" @click="close">{{ t('qualityOps.cancel') }}</button><button :form="formId" type="submit" class="btn btn-primary" data-testid="account-quality-save" :disabled="busy">{{ busy ? t('qualityOps.saving') : t('qualityOps.save') }}</button></div></template>
  </BaseDialog>

  <BaseDialog :show="confirmDelete" :title="t('qualityOps.deleteRuleTitle')" width="narrow" :z-index="70" :close-on-escape="!busy" :show-close-button="!busy" @close="!busy && (confirmDelete = false)">
    <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('qualityOps.deleteConfirm') }}</p>
    <p v-if="deleteError" class="editor-error mt-3" role="alert">{{ deleteError }}</p>
    <template #footer><div class="flex justify-end gap-2"><button type="button" class="btn btn-secondary" :disabled="busy" @click="confirmDelete = false">{{ t('qualityOps.cancel') }}</button><button type="button" class="btn bg-red-600 text-white hover:bg-red-700" :disabled="busy" data-testid="account-quality-confirm-delete" @click="remove">{{ busy ? t('qualityOps.deleting') : t('qualityOps.delete') }}</button></div></template>
  </BaseDialog>
  <BaseDialog :show="discardPrompt" :title="t('qualityOps.unsavedTitle')" width="narrow" :z-index="70" @close="discardPrompt = false">
    <p>{{ t('qualityOps.unsavedHint') }}</p>
    <template #footer><div class="flex justify-end gap-2"><button type="button" class="btn btn-secondary" @click="discardPrompt = false">{{ t('qualityOps.keepEditing') }}</button><button type="button" class="btn btn-primary" data-testid="account-quality-discard" @click="discardPrompt = false; emit('close')">{{ t('qualityOps.discard') }}</button></div></template>
  </BaseDialog>
</template>

<script setup lang="ts">
// Create or edit one account's quality rule. Saves immediately and
// independently of the account form it is opened from.
import { computed, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import QualityRuleFields from './QualityRuleFields.vue'
import scheduledTests from '@/api/admin/scheduledTests'
import { listQualityPlans } from '@/api/admin/accountQuality'
import { getAllIncludingInactive } from '@/api/admin/groups'
import {
  defaultQualityRuleForm,
  qualityRuleFormFromPlan,
  qualityRulePayload,
  qualityRuleScope,
  type QualityRuleForm,
  type QualityRulePayload,
  type QualityRuleScope
} from '@/utils/qualityRulePatch'
import type { AdminGroup, ScheduledTestPlan } from '@/types'

const props = defineProps<{
  show: boolean
  accountId: number
  accountName?: string
  // null creates a new rule for the account.
  plan: ScheduledTestPlan | null
  // This account's existing quality rules, for the one-rule-per-scope limit.
  accountPlans: ScheduledTestPlan[]
}>()
const emit = defineEmits<{ close: []; saved: [plan: ScheduledTestPlan]; deleted: [planId: number] }>()

const { t, te } = useI18n()
const formId = `account-quality-form-${useId()}`

const form = ref<QualityRuleForm>(defaultQualityRuleForm(''))
const groups = ref<AdminGroup[]>([]), groupsError = ref('')
const formError = ref(''), deleteError = ref('')
const busy = ref(false), confirmDelete = ref(false), discardPrompt = ref(false)
const initialForm = ref('')
const copySources = ref<ScheduledTestPlan[]>([]), copySourceId = ref<number | null>(null)
let openVersion = 0

const takenScopes = computed<QualityRuleScope[]>(() => props.accountPlans
  .filter(p => p.id !== props.plan?.id)
  .map(p => qualityRuleScope(p.pelican_config?.quality?.action)))
const copySourceOptions = computed(() => copySources.value.map(p => ({
  value: p.id,
  label: `${p.account_name || `#${p.account_id}`} · ${p.pelican_config?.rule_name || (p.pelican_config?.model_ids?.length ? p.pelican_config.model_ids : [p.model_id]).join(', ')}`
})))

function message(e: unknown) {
  const err = e as { response?: { data?: { message?: string; error?: string } }; message?: string }
  return err?.response?.data?.message || err?.response?.data?.error || err?.message || t('qualityOps.error')
}
function errorText(e: unknown) {
  const key = e instanceof Error ? e.message : ''
  return key && te(key) ? t(key) : message(e)
}

async function loadGroups() {
  groupsError.value = ''
  try { groups.value = await getAllIncludingInactive() } catch (e) { groupsError.value = message(e) }
}
async function loadCopySources(version: number) {
  try {
    const plans = await listQualityPlans()
    if (version === openVersion) copySources.value = plans.filter(p => p.account_id !== props.accountId)
  } catch { /* Copying is optional; the editor works without it. */ }
}

watch(() => props.show, show => {
  if (!show) return
  const version = ++openVersion
  const judgePrompt = t('qualityOps.defaultJudgePrompt')
  formError.value = ''; deleteError.value = ''; confirmDelete.value = false; discardPrompt.value = false
  copySourceId.value = null; copySources.value = []
  form.value = props.plan ? qualityRuleFormFromPlan(props.plan, judgePrompt) : defaultQualityRuleForm(judgePrompt)
  // A new rule starts in the scope this account still has free.
  if (!props.plan && takenScopes.value.includes('observation')) form.value.action = 'disable_scheduling'
  initialForm.value = JSON.stringify(form.value)
  if (!groups.value.length) void loadGroups()
  void loadCopySources(version)
}, { immediate: true })

// Copy what peer comparison needs to match; keep this account's own action settings.
function copyFromSource() {
  const source = copySources.value.find(p => p.id === copySourceId.value)
  if (!source) return
  const copied = qualityRuleFormFromPlan(source, t('qualityOps.defaultJudgePrompt'))
  form.value = {
    ...form.value,
    models: copied.models,
    question_kind: copied.question_kind,
    prompt: copied.prompt,
    expected_answer: copied.expected_answer,
    reasoning_effort: copied.reasoning_effort,
    cron_expression: copied.cron_expression,
    parallel_count: copied.parallel_count,
    judge: { ...copied.judge }
  }
}

function close() {
  if (busy.value) return
  if (initialForm.value !== JSON.stringify(form.value)) { discardPrompt.value = true; return }
  emit('close')
}

async function save() {
  if (busy.value) return
  formError.value = ''
  let body: QualityRulePayload
  try { body = qualityRulePayload(form.value) } catch (e) { formError.value = errorText(e); return }
  if (takenScopes.value.includes(qualityRuleScope(form.value.action))) { formError.value = t('qualityOps.accountSection.scopeConflict'); return }
  busy.value = true
  try {
    const saved = props.plan
      ? await scheduledTests.update(props.plan.id, body)
      : await scheduledTests.create({ ...body, account_id: props.accountId })
    initialForm.value = JSON.stringify(form.value)
    emit('saved', saved)
  } catch (e) { formError.value = message(e) }
  finally { busy.value = false }
}

async function remove() {
  if (!props.plan || busy.value) return
  busy.value = true; deleteError.value = ''
  try {
    await scheduledTests.delete(props.plan.id)
    confirmDelete.value = false
    emit('deleted', props.plan.id)
  } catch (e) { deleteError.value = message(e) }
  finally { busy.value = false }
}
</script>

<style scoped>
.editor-footer { @apply flex w-full items-center gap-2; }
.editor-error { @apply mb-4 rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300; }
.delete-rule { @apply text-sm text-red-600; }
.quality-editor label, .quality-editor .space-y-1 > span { @apply text-sm; }
.editor-account { @apply flex flex-wrap items-baseline gap-2 rounded-lg bg-gray-50 px-3 py-2 text-sm dark:bg-dark-800; }
.editor-account span:last-child { @apply text-xs text-gray-500; }
button:disabled { @apply cursor-not-allowed opacity-40; }
</style>
