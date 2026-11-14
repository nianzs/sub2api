<template>
  <div class="border-t border-gray-200 pt-4 dark:border-dark-600" data-testid="account-quality-section">
    <div class="flex items-start justify-between gap-4">
      <div class="min-w-0">
        <label class="input-label mb-0">{{ t('qualityOps.accountSection.title') }}</label>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('qualityOps.accountSection.hint') }}</p>
      </div>
      <button v-if="loaded && !full" type="button" class="btn btn-secondary btn-sm shrink-0" data-testid="account-quality-add" @click="openEditor(null)">{{ t('qualityOps.accountSection.add') }}</button>
    </div>

    <p v-if="error" class="mt-3 text-sm text-red-600" role="alert">{{ error }} <button type="button" class="underline" @click="load">{{ t('qualityOps.retry') }}</button></p>
    <p v-if="notice" class="mt-3 text-sm text-emerald-600" role="status">{{ notice }}</p>
    <p v-if="!loaded && loading" class="mt-3 text-sm text-gray-500" role="status">{{ t('qualityOps.loading') }}</p>
    <p v-else-if="loaded && !plans.length" class="mt-3 text-sm text-gray-500" data-testid="account-quality-empty">{{ t('qualityOps.accountSection.empty') }}</p>

    <div v-for="plan in plans" :key="plan.id" class="mt-3 rounded-lg border border-gray-200 p-3 text-sm dark:border-dark-600" :data-plan-id="plan.id" data-testid="account-quality-rule">
      <div class="flex flex-wrap items-center gap-2">
        <span class="rounded bg-gray-100 px-1.5 py-0.5 text-xs text-gray-600 dark:bg-dark-700 dark:text-gray-300">{{ t(qualityRuleScope(plan.pelican_config?.quality?.action) === 'observation' ? 'qualityOps.accountSection.observation' : 'qualityOps.accountSection.quarantine') }}</span>
        <strong class="min-w-0 break-all">{{ plan.pelican_config?.rule_name || ruleModels(plan).join(', ') }}</strong>
        <span class="text-xs" :class="plan.enabled ? 'text-emerald-600' : 'text-gray-500'">{{ t(plan.enabled ? 'qualityOps.activeShort' : 'qualityOps.paused') }}</span>
        <span v-if="running(plan)" class="text-xs text-primary-600">{{ t('qualityOps.running') }}</span>
      </div>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
        {{ actionText(plan) }} · {{ ruleModels(plan).join(', ') }} · {{ t('qualityOps.nextRun') }} {{ plan.enabled ? date(plan.next_run_at) : '—' }}
      </p>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400" data-testid="account-quality-last-result">
        {{ t('qualityOps.accountSection.lastResult') }}:
        <template v-if="lastResults[plan.id]">{{ resultText(lastResults[plan.id]!) }} · {{ date(lastResults[plan.id]!.started_at) }}</template>
        <template v-else>{{ t('qualityOps.accountSection.noResult') }}</template>
      </p>
      <div class="mt-2 flex flex-wrap gap-3 text-xs">
        <button type="button" class="text-primary-600 disabled:opacity-40" :disabled="!!pending[plan.id]" data-testid="account-quality-edit" @click="openEditor(plan)">{{ t('qualityOps.editShort') }}</button>
        <button type="button" class="text-primary-600 disabled:opacity-40" :disabled="!!pending[plan.id]" data-testid="account-quality-toggle" @click="toggle(plan)">{{ t(plan.enabled ? 'qualityOps.pause' : 'qualityOps.enable') }}</button>
        <button type="button" class="text-primary-600 disabled:opacity-40" :disabled="!!pending[plan.id] || !plan.enabled || running(plan)" data-testid="account-quality-run" @click="run(plan)">{{ t('qualityOps.runShort') }}</button>
        <a class="text-primary-600" :href="historyHref(plan)" target="_blank" rel="noopener" data-testid="account-quality-history">{{ t('qualityOps.accountSection.viewHistory') }}</a>
      </div>
    </div>

    <AccountQualityRuleDialog
      :show="editorOpen"
      :account-id="account.id"
      :account-name="account.name"
      :plan="editingPlan"
      :account-plans="plans"
      @close="editorOpen = false"
      @saved="onSaved"
      @deleted="onDeleted"
    />
  </div>
</template>

<script setup lang="ts">
// Quality rules of the account being edited. Rules save on their own and are
// not part of the account form's save or cancel.
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import AccountQualityRuleDialog from '@/components/admin/quality/AccountQualityRuleDialog.vue'
import scheduledTests from '@/api/admin/scheduledTests'
import { runQualityPlan } from '@/api/admin/accountQuality'
import { qualityRuleScope, qualityUsesGroups } from '@/utils/qualityRulePatch'
import type { ScheduledTestPlan, ScheduledTestResult } from '@/types'

const props = defineProps<{ account: { id: number; name?: string } }>()

const { t, te } = useI18n()
const router = useRouter()

const plans = ref<ScheduledTestPlan[]>([])
const lastResults = ref<Record<number, ScheduledTestResult | undefined>>({})
const loaded = ref(false), loading = ref(false), error = ref(''), notice = ref('')
const pending = ref<Record<number, boolean>>({})
const editorOpen = ref(false), editingPlan = ref<ScheduledTestPlan | null>(null)
let version = 0

// One observation rule and one quarantine rule per account.
const full = computed(() => new Set(plans.value.map(p => qualityRuleScope(p.pelican_config?.quality?.action))).size >= 2)

function message(e: unknown) {
  const err = e as { response?: { data?: { message?: string; error?: string } }; message?: string }
  return err?.response?.data?.message || err?.response?.data?.error || err?.message || t('qualityOps.error')
}
function date(value: string | null | undefined) {
  return value ? new Date(value).toLocaleString(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }) : '—'
}
function running(plan: ScheduledTestPlan) { return !!plan.running_until && Date.parse(plan.running_until) > Date.now() }
function ruleModels(plan: ScheduledTestPlan) { return plan.pelican_config?.model_ids?.length ? plan.pelican_config.model_ids : [plan.model_id] }
function actionText(plan: ScheduledTestPlan) {
  const quality = plan.pelican_config?.quality
  if (!quality || quality.action === 'observe_only') return t('qualityOps.observeOnly')
  if (qualityUsesGroups(quality.action)) return t('qualityOps.accountSection.removeGroupsCount', { count: quality.remove_group_ids.length })
  return t('qualityOps.disableSchedulingShort')
}
function resultText(result: ScheduledTestResult) {
  const key = `qualityOps.outcomes.${result.quality_action}`
  if (result.quality_action && te(key)) return t(key)
  return t(result.status === 'success' ? 'qualityOps.passed' : result.status === 'skipped' ? 'qualityOps.skipped' : 'qualityOps.roundNotPassed')
}
// Opens in a new tab so unsaved account edits are not lost.
function historyHref(plan: ScheduledTestPlan) {
  const location = { path: '/admin/account-quality', query: { plan: String(plan.id) } }
  return router ? router.resolve(location).href : `${location.path}?plan=${plan.id}`
}

async function loadLastResult(plan: ScheduledTestPlan, request: number) {
  try {
    const [latest] = await scheduledTests.listResults(plan.id, 1, false)
    if (request === version) lastResults.value = { ...lastResults.value, [plan.id]: latest }
  } catch { /* The rule list stays useful without its latest result. */ }
}
async function load() {
  const request = ++version
  loading.value = true; error.value = ''
  try {
    const all = await scheduledTests.listByAccount(props.account.id)
    if (request !== version) return
    plans.value = all.filter(p => !!p.pelican_config?.quality)
    lastResults.value = {}
    loaded.value = true
    for (const plan of plans.value) void loadLastResult(plan, request)
  } catch (e) { if (request === version) error.value = message(e) }
  finally { if (request === version) loading.value = false }
}
watch(() => props.account.id, () => {
  plans.value = []; loaded.value = false; notice.value = ''; editorOpen.value = false
  void load()
}, { immediate: true })
onBeforeUnmount(() => { version++ })

async function act(plan: ScheduledTestPlan, fn: () => Promise<unknown>, done?: string) {
  if (pending.value[plan.id]) return
  pending.value = { ...pending.value, [plan.id]: true }; error.value = ''; notice.value = ''
  try {
    await fn()
    if (done) notice.value = done
    await load()
  } catch (e) { error.value = message(e) }
  finally { const next = { ...pending.value }; delete next[plan.id]; pending.value = next }
}
function toggle(plan: ScheduledTestPlan) { return act(plan, () => scheduledTests.update(plan.id, { enabled: !plan.enabled })) }
function run(plan: ScheduledTestPlan) { return act(plan, () => runQualityPlan(plan.id), t('qualityOps.queued')) }

function openEditor(plan: ScheduledTestPlan | null) {
  editingPlan.value = plan; notice.value = ''; editorOpen.value = true
}
function onSaved() { editorOpen.value = false; notice.value = t('qualityOps.saved'); void load() }
function onDeleted() { editorOpen.value = false; notice.value = t('qualityOps.ruleDeleted'); void load() }
</script>
