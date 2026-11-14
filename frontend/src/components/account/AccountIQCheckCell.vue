<template>
  <span v-if="status === 'off'" class="text-sm text-gray-400 dark:text-gray-500" data-testid="iq-check-off">-</span>
  <div v-else class="flex flex-col items-start gap-1">
    <button
      v-if="drawing"
      type="button"
      class="flex h-12 w-16 items-center justify-center overflow-hidden rounded border border-gray-200 bg-white hover:border-primary-300 dark:border-dark-600 dark:bg-dark-800"
      :title="drawing.error ? t('admin.accounts.iqCheck.details.error', { error: drawing.error }) : t('admin.accounts.iqCheck.drawing.view')"
      data-testid="iq-check-thumbnail"
      @click="showDrawing = true"
    >
      <!-- Only <img>: the browser runs no script and loads nothing external from an SVG image. -->
      <img v-if="drawing.svg" :src="iqCheckDrawingSrc(drawing.svg)" alt="" class="h-full w-full object-contain" />
      <span v-else class="px-1 text-[10px] text-gray-400" data-testid="iq-check-drawing-failed">{{ t('admin.accounts.iqCheck.drawing.failed') }}</span>
    </button>
    <HelpTooltip width-class="w-max max-w-[calc(100vw-2rem)]" data-testid="iq-check-details">
      <template #trigger>
        <span
          class="inline-flex cursor-help items-center whitespace-nowrap rounded px-1.5 py-0.5 text-xs font-medium"
          :class="badgeClass"
          data-testid="iq-check-status"
        >
          {{ label }}
        </span>
      </template>
      <div class="space-y-1">
        <p data-testid="iq-check-mode">{{ modeText }}</p>
        <p v-if="state?.checked_at">{{ t('admin.accounts.iqCheck.details.checkedAt', { time: formatDateTime(state.checked_at) }) }}</p>
        <p v-if="state?.total">{{ t('admin.accounts.iqCheck.details.score', { correct: state.correct ?? 0, total: state.total }) }}</p>
        <p v-if="state?.checked_at">{{ t('admin.accounts.iqCheck.details.recent', { wrong: state.recent_wrong ?? 0 }) }}</p>
        <p v-if="state?.answered" data-testid="iq-check-accuracy">
          {{ t('admin.accounts.iqCheck.details.accuracy', { correct: state.answered_correct ?? 0, answered: state.answered, percent: accuracyPercent }) }}
        </p>
        <p v-if="state?.model">{{ t('admin.accounts.iqCheck.details.model', { model: state.model }) }}</p>
        <p v-if="state?.error" class="break-all" data-testid="iq-check-error">{{ t('admin.accounts.iqCheck.details.error', { error: state.error }) }}</p>
        <p v-if="enabled && state?.next_check_at">{{ t('admin.accounts.iqCheck.details.nextCheckAt', { time: formatDateTime(state.next_check_at) }) }}</p>
      </div>
    </HelpTooltip>
    <span v-if="drawing" class="whitespace-nowrap text-[11px] text-gray-400 dark:text-gray-500" data-testid="iq-check-drawn-at">
      {{ formatRelativeTime(drawing.drawn_at) }} · {{ iqCheckDrawingSeconds(drawing) }}s
    </span>
    <IQCheckDrawingDialog :show="showDrawing" :drawing="drawing ?? null" @close="showDrawing = false" />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import IQCheckDrawingDialog from './IQCheckDrawingDialog.vue'
import { formatDateTime, formatRelativeTime } from '@/utils/format'
import { iqCheckDrawingSeconds, iqCheckDrawingSrc } from '@/utils/iqCheckDrawing'
import type { Account, IQCheckDrawing } from '@/types'

// Matches iqCheckWrongToDisable in backend/internal/service/iq_check_service.go.
const WRONG_TO_DISABLE = 3

type Status = 'off' | 'disabled' | 'pending' | 'triggered' | 'wrong' | 'passed' | 'unknown'

const props = defineProps<{ account: Pick<Account, 'extra'>; drawing?: IQCheckDrawing | null }>()
const { t } = useI18n()
const showDrawing = ref(false)

const enabled = computed(() => props.account.extra?.iq_check_enabled === true)
const state = computed(() => props.account.extra?.iq_check_state)

const status = computed<Status>(() => {
  const current = state.value
  // A check-stopped account stays visible even after the check is turned off.
  if (current?.disabled_by_check) return 'disabled'
  if (!enabled.value) return 'off'
  if (!current?.checked_at) return 'pending'
  if ((current.recent_wrong ?? 0) >= WRONG_TO_DISABLE) return 'triggered'
  if (current.result === 'failed') return 'wrong'
  if (current.result === 'passed') return 'passed'
  return 'unknown'
})

const label = computed(() => {
  if (status.value === 'wrong') {
    const current = state.value
    // A failed round always has answers, so last_round_wrong is this round's count.
    return t('admin.accounts.iqCheck.status.wrong', { wrong: Math.max(current?.last_round_wrong ?? 1, 1), total: current?.total ?? 0 })
  }
  return t(`admin.accounts.iqCheck.status.${status.value}`)
})

const modeText = computed(() => {
  if (!enabled.value) return t('admin.accounts.iqCheck.details.off')
  const mode = props.account.extra?.iq_check_auto_disable === true ? 'auto' : 'record'
  return t('admin.accounts.iqCheck.details.mode', { mode: t(`admin.accounts.iqCheck.modes.${mode}`) })
})

const accuracyPercent = computed(() => {
  const answered = state.value?.answered ?? 0
  return answered > 0 ? Math.round(((state.value?.answered_correct ?? 0) / answered) * 100) : 0
})

const badgeClass = computed(() => ({
  off: '',
  pending: 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300',
  passed: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300',
  wrong: 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300',
  triggered: 'bg-orange-50 text-orange-700 dark:bg-orange-900/30 dark:text-orange-300',
  disabled: 'bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-300',
  unknown: 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
}[status.value]))
</script>
