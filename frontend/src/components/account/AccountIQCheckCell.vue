<template>
  <span v-if="!enabled" class="text-sm text-gray-400 dark:text-gray-500" data-testid="iq-check-off">-</span>
  <HelpTooltip v-else width-class="w-max max-w-[calc(100vw-2rem)]" data-testid="iq-check-details">
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
      <p v-if="state?.checked_at">{{ t('admin.accounts.iqCheck.details.checkedAt', { time: formatDateTime(state.checked_at) }) }}</p>
      <p v-if="state?.total">{{ t('admin.accounts.iqCheck.details.score', { correct: state.correct ?? 0, total: state.total }) }}</p>
      <p v-if="state?.model">{{ t('admin.accounts.iqCheck.details.model', { model: state.model }) }}</p>
      <p v-if="state?.error" class="break-all" data-testid="iq-check-error">{{ t('admin.accounts.iqCheck.details.error', { error: state.error }) }}</p>
      <p v-if="state?.next_check_at">{{ t('admin.accounts.iqCheck.details.nextCheckAt', { time: formatDateTime(state.next_check_at) }) }}</p>
      <p v-if="!state?.checked_at">{{ t('admin.accounts.iqCheck.status.pending') }}</p>
    </div>
  </HelpTooltip>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import { formatDateTime } from '@/utils/format'
import type { Account } from '@/types'

// Matches iqCheckFailuresToDisable in backend/internal/service/iq_check_service.go.
const FAILURES_TO_DISABLE = 2

const props = defineProps<{ account: Pick<Account, 'extra'> }>()
const { t } = useI18n()

const enabled = computed(() => props.account.extra?.iq_check_enabled === true)
const state = computed(() => props.account.extra?.iq_check_state)

const status = computed<'pending' | 'passed' | 'suspect' | 'disabled' | 'unknown'>(() => {
  const current = state.value
  if (!current?.checked_at) return 'pending'
  if (current.disabled_by_check) return 'disabled'
  if (current.result === 'passed') return 'passed'
  if (current.result === 'failed') return 'suspect'
  // An unknown round keeps earlier failures; still show them as suspected.
  if ((current.consecutive_failures ?? 0) > 0) return 'suspect'
  return 'unknown'
})

const label = computed(() => status.value === 'suspect'
  ? t('admin.accounts.iqCheck.status.suspect', {
      // An account someone else turned off keeps counting; cap the display at the limit.
      count: Math.min(Math.max(state.value?.consecutive_failures ?? 1, 1), FAILURES_TO_DISABLE),
      limit: FAILURES_TO_DISABLE
    })
  : t(`admin.accounts.iqCheck.status.${status.value}`))

const badgeClass = computed(() => ({
  pending: 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300',
  passed: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300',
  suspect: 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300',
  disabled: 'bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-300',
  unknown: 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
}[status.value]))
</script>
