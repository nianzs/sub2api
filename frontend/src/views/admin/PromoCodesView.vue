<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="flex flex-wrap items-center gap-3">
          <!-- Left: Search + Filters -->
          <div class="flex-1 sm:max-w-64">
            <input
              v-model="searchQuery"
              type="text"
              :placeholder="t('admin.promo.searchCodes')"
              class="input"
              @input="handleSearch"
            />
          </div>
          <Select
            v-model="filters.status"
            :options="filterStatusOptions"
            class="w-36"
            @change="loadCodes"
          />

          <!-- Right: Action buttons -->
          <div class="flex flex-1 flex-wrap items-center justify-end gap-2">
            <button
              @click="loadCodes"
              :disabled="loading"
              class="btn btn-secondary"
              :title="t('common.refresh')"
            >
              <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
            </button>
            <button @click="showCreateDialog = true" class="btn btn-primary">
              <Icon name="plus" size="md" class="mr-1" />
              {{ t('admin.promo.createCode') }}
            </button>
          </div>
        </div>
      </template>

      <template #table>
        <DataTable
          :columns="columns"
          :data="codes"
          :loading="loading"
          :server-side-sort="true"
          default-sort-key="created_at"
          default-sort-order="desc"
          @sort="handleSort"
        >
          <template #cell-code="{ value }">
            <div class="flex items-center space-x-2">
              <code class="font-mono text-sm text-gray-900 dark:text-gray-100">{{ value }}</code>
              <button
                @click="copyToClipboard(value)"
                :class="[
                  'flex items-center transition-colors',
                  copiedCode === value
                    ? 'text-green-500'
                    : 'text-gray-400 hover:text-gray-600 dark:hover:text-gray-300'
                ]"
                :title="copiedCode === value ? t('admin.promo.copied') : t('keys.copyToClipboard')"
              >
                <Icon v-if="copiedCode !== value" name="copy" size="sm" :stroke-width="2" />
                <svg v-else class="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M5 13l4 4L19 7"
                  />
                </svg>
              </button>
            </div>
          </template>

          <template #cell-bonus_amount="{ value }">
            <span class="text-sm font-medium text-gray-900 dark:text-white">
              ${{ value.toFixed(2) }}
            </span>
          </template>

          <template #cell-first_recharge_promo="{ row }">
            <span v-if="formatFirstRechargePromo(row)" class="text-sm text-gray-700 dark:text-gray-300">
              {{ formatFirstRechargePromo(row) }}
            </span>
            <span v-else class="text-sm text-gray-400">{{ t('admin.promo.noFirstRechargePromo') }}</span>
          </template>

          <template #cell-usage="{ row }">
            <span class="text-sm text-gray-600 dark:text-gray-300">
              {{ row.used_count }} / {{ row.max_uses === 0 ? '∞' : row.max_uses }}
            </span>
          </template>

          <template #cell-status="{ value, row }">
            <span
              :class="[
                'badge',
                getStatusClass(value, row)
              ]"
            >
              {{ getStatusLabel(value, row) }}
            </span>
          </template>

          <template #cell-expires_at="{ value }">
            <span class="text-sm text-gray-500 dark:text-dark-400">
              {{ value ? formatDateTime(value) : t('admin.promo.neverExpires') }}
            </span>
          </template>

          <template #cell-created_at="{ value }">
            <span class="text-sm text-gray-500 dark:text-dark-400">
              {{ formatDateTime(value) }}
            </span>
          </template>

          <template #cell-actions="{ row }">
            <div class="flex items-center space-x-1">
              <button
                @click="copyRegisterLink(row)"
                class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-green-50 hover:text-green-600 dark:hover:bg-green-900/20 dark:hover:text-green-400"
                :title="t('admin.promo.copyRegisterLink')"
              >
                <Icon name="link" size="sm" />
              </button>
              <button
                @click="handleViewUsages(row)"
                class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-blue-50 hover:text-blue-600 dark:hover:bg-blue-900/20 dark:hover:text-blue-400"
                :title="t('admin.promo.viewUsages')"
              >
                <Icon name="eye" size="sm" />
              </button>
              <button
                @click="handleEdit(row)"
                class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:hover:bg-dark-600 dark:hover:text-gray-300"
                :title="t('common.edit')"
              >
                <Icon name="edit" size="sm" />
              </button>
              <button
                @click="handleDelete(row)"
                class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400"
                :title="t('common.delete')"
              >
                <Icon name="trash" size="sm" />
              </button>
            </div>
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="pagination.total > 0"
          :page="pagination.page"
          :total="pagination.total"
          :page-size="pagination.page_size"
          @update:page="handlePageChange"
          @update:pageSize="handlePageSizeChange"
        />
      </template>
    </TablePageLayout>

    <!-- Create Dialog -->
    <BaseDialog
      :show="showCreateDialog"
      :title="t('admin.promo.createCode')"
      width="normal"
      @close="showCreateDialog = false"
    >
      <form id="create-promo-form" @submit.prevent="handleCreate" class="space-y-4">
        <div>
          <label class="input-label">
            {{ t('admin.promo.code') }}
            <span class="ml-1 text-xs font-normal text-gray-400">({{ t('admin.promo.autoGenerate') }})</span>
          </label>
          <input
            v-model="createForm.code"
            type="text"
            class="input font-mono uppercase"
            :placeholder="t('admin.promo.codePlaceholder')"
          />
        </div>
        <div>
          <label class="input-label">{{ t('admin.promo.bonusAmount') }}</label>
          <input
            v-model.number="createForm.bonus_amount"
            type="number"
            step="0.01"
            min="0"
            required
            class="input"
          />
        </div>
        <div class="grid gap-4 sm:grid-cols-3">
          <div>
            <label class="input-label">
              {{ t('admin.promo.firstRechargeBonusAmount') }}
              <span class="ml-1 text-xs font-normal text-gray-400">({{ t('common.optional') }})</span>
            </label>
            <input
              v-model.number="createForm.first_recharge_bonus_amount"
              type="number"
              step="0.01"
              min="0"
              class="input"
              :placeholder="t('admin.promo.firstRechargeBonusPlaceholder')"
            />
          </div>
          <div>
            <label class="input-label">
              {{ t('admin.promo.firstRechargeDiscountRate') }}
              <span class="ml-1 text-xs font-normal text-gray-400">({{ t('common.optional') }})</span>
            </label>
            <div class="relative">
              <input
                v-model.number="createForm.first_recharge_discount_rate"
                type="number"
                step="0.01"
                min="0.01"
                max="10"
                class="input pr-8"
                :placeholder="t('admin.promo.firstRechargeDiscountPlaceholder')"
              />
              <span class="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-gray-400">{{ t('admin.promo.discountUnit') }}</span>
            </div>
          </div>
          <div>
            <label class="input-label">
              {{ t('admin.promo.rechargeDiscountTimes') }}
            </label>
            <input
              v-model.number="createForm.first_recharge_discount_times"
              type="number"
              min="0"
              class="input"
              :placeholder="t('admin.promo.rechargeDiscountTimesPlaceholder')"
            />
          </div>
        </div>
        <div>
          <label class="input-label">
            {{ t('admin.promo.maxUses') }}
            <span class="ml-1 text-xs font-normal text-gray-400">({{ t('admin.promo.zeroUnlimited') }})</span>
          </label>
          <input
            v-model.number="createForm.max_uses"
            type="number"
            min="0"
            class="input"
          />
        </div>
        <div>
          <label class="input-label">
            {{ t('admin.promo.expiresAt') }}
            <span class="ml-1 text-xs font-normal text-gray-400">({{ t('common.optional') }})</span>
          </label>
          <input
            v-model="createForm.expires_at_str"
            type="datetime-local"
            class="input"
          />
        </div>
        <div>
          <label class="input-label">
            {{ t('admin.promo.notes') }}
            <span class="ml-1 text-xs font-normal text-gray-400">({{ t('common.optional') }})</span>
          </label>
          <textarea
            v-model="createForm.notes"
            rows="2"
            class="input"
            :placeholder="t('admin.promo.notesPlaceholder')"
          ></textarea>
        </div>
      </form>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" @click="showCreateDialog = false" class="btn btn-secondary">
            {{ t('common.cancel') }}
          </button>
          <button type="submit" form="create-promo-form" :disabled="creating" class="btn btn-primary">
            {{ creating ? t('common.creating') : t('common.create') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Edit Dialog -->
    <BaseDialog
      :show="showEditDialog"
      :title="t('admin.promo.editCode')"
      width="normal"
      @close="closeEditDialog"
    >
      <form id="edit-promo-form" @submit.prevent="handleUpdate" class="space-y-4">
        <div>
          <label class="input-label">{{ t('admin.promo.code') }}</label>
          <input
            v-model="editForm.code"
            type="text"
            class="input font-mono uppercase"
          />
        </div>
        <div>
          <label class="input-label">{{ t('admin.promo.bonusAmount') }}</label>
          <input
            v-model.number="editForm.bonus_amount"
            type="number"
            step="0.01"
            min="0"
            required
            class="input"
          />
        </div>
        <div class="grid gap-4 sm:grid-cols-3">
          <div>
            <label class="input-label">
              {{ t('admin.promo.firstRechargeBonusAmount') }}
              <span class="ml-1 text-xs font-normal text-gray-400">({{ t('common.optional') }})</span>
            </label>
            <input
              v-model.number="editForm.first_recharge_bonus_amount"
              type="number"
              step="0.01"
              min="0"
              class="input"
              :placeholder="t('admin.promo.firstRechargeBonusPlaceholder')"
            />
          </div>
          <div>
            <label class="input-label">
              {{ t('admin.promo.firstRechargeDiscountRate') }}
              <span class="ml-1 text-xs font-normal text-gray-400">({{ t('common.optional') }})</span>
            </label>
            <div class="relative">
              <input
                v-model.number="editForm.first_recharge_discount_rate"
                type="number"
                step="0.01"
                min="0.01"
                max="10"
                class="input pr-8"
                :placeholder="t('admin.promo.firstRechargeDiscountPlaceholder')"
              />
              <span class="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-gray-400">{{ t('admin.promo.discountUnit') }}</span>
            </div>
          </div>
          <div>
            <label class="input-label">
              {{ t('admin.promo.rechargeDiscountTimes') }}
            </label>
            <input
              v-model.number="editForm.first_recharge_discount_times"
              type="number"
              min="0"
              class="input"
              :placeholder="t('admin.promo.rechargeDiscountTimesPlaceholder')"
            />
          </div>
        </div>
        <div>
          <label class="input-label">
            {{ t('admin.promo.maxUses') }}
            <span class="ml-1 text-xs font-normal text-gray-400">({{ t('admin.promo.zeroUnlimited') }})</span>
          </label>
          <input
            v-model.number="editForm.max_uses"
            type="number"
            min="0"
            class="input"
          />
        </div>
        <div>
          <label class="input-label">{{ t('admin.promo.status') }}</label>
          <Select v-model="editForm.status" :options="statusOptions" />
        </div>
        <div>
          <label class="input-label">
            {{ t('admin.promo.expiresAt') }}
            <span class="ml-1 text-xs font-normal text-gray-400">({{ t('common.optional') }})</span>
          </label>
          <input
            v-model="editForm.expires_at_str"
            type="datetime-local"
            class="input"
          />
        </div>
        <div>
          <label class="input-label">
            {{ t('admin.promo.notes') }}
            <span class="ml-1 text-xs font-normal text-gray-400">({{ t('common.optional') }})</span>
          </label>
          <textarea
            v-model="editForm.notes"
            rows="2"
            class="input"
          ></textarea>
        </div>
      </form>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" @click="closeEditDialog" class="btn btn-secondary">
            {{ t('common.cancel') }}
          </button>
          <button type="submit" form="edit-promo-form" :disabled="updating" class="btn btn-primary">
            {{ updating ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Usages Dialog -->
    <BaseDialog
      :show="showUsagesDialog"
      :title="t('admin.promo.usageRecords')"
      width="wide"
      @close="showUsagesDialog = false"
    >
      <div v-if="usagesLoading" class="flex items-center justify-center py-8">
        <Icon name="refresh" size="lg" class="animate-spin text-gray-400" />
      </div>
      <div v-else-if="usages.length === 0" class="py-8 text-center text-gray-500 dark:text-gray-400">
        {{ t('admin.promo.noUsages') }}
      </div>
      <div v-else class="space-y-3">
        <div
          v-for="usage in usages"
          :key="usage.id"
          class="flex items-center justify-between rounded-lg border border-gray-200 p-3 dark:border-dark-600"
        >
          <div class="flex items-center gap-3">
            <div class="flex h-8 w-8 items-center justify-center rounded-full bg-green-100 dark:bg-green-900/30">
              <Icon name="user" size="sm" class="text-green-600 dark:text-green-400" />
            </div>
            <div>
              <p class="text-sm font-medium text-gray-900 dark:text-white">
                {{ usage.user?.email || t('admin.promo.userPrefix', { id: usage.user_id }) }}
              </p>
              <p class="text-xs text-gray-500 dark:text-gray-400">
                {{ formatDateTime(usage.used_at) }}
              </p>
            </div>
          </div>
          <div class="text-right">
            <span class="text-sm font-medium text-green-600 dark:text-green-400">
              +${{ usage.bonus_amount.toFixed(2) }}
            </span>
          </div>
        </div>
        <!-- Usages Pagination -->
        <div v-if="usagesTotal > usagesPageSize" class="mt-4">
          <Pagination
            :page="usagesPage"
            :total="usagesTotal"
            :page-size="usagesPageSize"
            @update:page="handleUsagesPageChange"
            @update:page-size="(size: number) => { usagesPageSize = size; usagesPage = 1; loadUsages() }"
          />
        </div>
      </div>
      <template #footer>
        <div class="flex justify-end">
          <button type="button" @click="showUsagesDialog = false" class="btn btn-secondary">
            {{ t('common.close') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Delete Confirmation Dialog -->
    <ConfirmDialog
      :show="showDeleteDialog"
      :title="t('admin.promo.deleteCode')"
      :message="t('admin.promo.deleteCodeConfirm')"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      danger
      @confirm="confirmDelete"
      @cancel="showDeleteDialog = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useClipboard } from '@/composables/useClipboard'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { adminAPI } from '@/api/admin'
import { formatDateTime } from '@/utils/format'
import type { CreatePromoCodeRequest, PromoCode, PromoCodeUsage, UpdatePromoCodeRequest } from '@/types'
import type { Column } from '@/components/common/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()
const appStore = useAppStore()
const { copyToClipboard: clipboardCopy } = useClipboard()

// State
const codes = ref<PromoCode[]>([])
const loading = ref(false)
const creating = ref(false)
const updating = ref(false)
const searchQuery = ref('')
const copiedCode = ref<string | null>(null)

const filters = reactive({
  status: ''
})

const pagination = reactive({
  page: 1,
  page_size: getPersistedPageSize(),
  total: 0
})
const sortState = reactive({
  sort_by: 'created_at',
  sort_order: 'desc' as 'asc' | 'desc'
})

// Dialogs
const showCreateDialog = ref(false)
const showEditDialog = ref(false)
const showDeleteDialog = ref(false)
const showUsagesDialog = ref(false)

const editingCode = ref<PromoCode | null>(null)
const deletingCode = ref<PromoCode | null>(null)

// Usages
const usages = ref<PromoCodeUsage[]>([])
const usagesLoading = ref(false)
const currentViewingCode = ref<PromoCode | null>(null)
const usagesPage = ref(1)
const usagesPageSize = ref(20)
const usagesTotal = ref(0)

// Forms
const createForm = reactive({
  code: '',
  bonus_amount: 1,
  first_recharge_bonus_amount: '' as string | number,
  first_recharge_discount_rate: '' as string | number,
  first_recharge_discount_times: 1 as string | number,
  max_uses: 0,
  expires_at_str: '',
  notes: ''
})

const editForm = reactive({
  code: '',
  bonus_amount: 0,
  first_recharge_bonus_amount: '' as string | number,
  first_recharge_discount_rate: '' as string | number,
  first_recharge_discount_times: 1 as string | number,
  max_uses: 0,
  status: 'active' as 'active' | 'disabled',
  expires_at_str: '',
  notes: ''
})

// Options
const filterStatusOptions = computed(() => [
  { value: '', label: t('admin.promo.allStatus') },
  { value: 'active', label: t('admin.promo.statusActive') },
  { value: 'disabled', label: t('admin.promo.statusDisabled') }
])

const statusOptions = computed(() => [
  { value: 'active', label: t('admin.promo.statusActive') },
  { value: 'disabled', label: t('admin.promo.statusDisabled') }
])

const columns = computed<Column[]>(() => [
  { key: 'code', label: t('admin.promo.columns.code') },
  { key: 'bonus_amount', label: t('admin.promo.columns.bonusAmount'), sortable: true },
  { key: 'first_recharge_promo', label: t('admin.promo.columns.firstRechargePromo') },
  { key: 'usage', label: t('admin.promo.columns.usage') },
  { key: 'status', label: t('admin.promo.columns.status'), sortable: true },
  { key: 'expires_at', label: t('admin.promo.columns.expiresAt'), sortable: true },
  { key: 'created_at', label: t('admin.promo.columns.createdAt'), sortable: true },
  { key: 'actions', label: t('admin.promo.columns.actions') }
])

// Helpers
const getStatusClass = (status: string, row: PromoCode) => {
  if (row.expires_at && new Date(row.expires_at) < new Date()) {
    return 'badge-danger'
  }
  if (row.max_uses > 0 && row.used_count >= row.max_uses) {
    return 'badge-gray'
  }
  return status === 'active' ? 'badge-success' : 'badge-gray'
}

const getStatusLabel = (status: string, row: PromoCode) => {
  if (row.expires_at && new Date(row.expires_at) < new Date()) {
    return t('admin.promo.statusExpired')
  }
  if (row.max_uses > 0 && row.used_count >= row.max_uses) {
    return t('admin.promo.statusMaxUsed')
  }
  return status === 'active' ? t('admin.promo.statusActive') : t('admin.promo.statusDisabled')
}

const parseOptionalNumber = (
  raw: unknown,
  min: number,
  max: number,
  messageKey: string
): number | null | undefined => {
  const s = String(raw ?? '').trim()
  if (s === '') return null
  const parsed = Number(s)
  if (Number.isNaN(parsed) || parsed < min || parsed > max) {
    appStore.showError(t(messageKey))
    return undefined
  }
  return parsed
}

const formatDiscountRate = (percent: number): string =>
  Number(percent / 10).toFixed(2).replace(/\.?0+$/, '')

const formatFirstRechargePromo = (code: PromoCode): string => {
  const parts: string[] = []
  const bonus = Number(code.first_recharge_bonus_amount || 0)
  const discount = Number(code.first_recharge_discount_percent || 0)
  if (bonus > 0) {
    parts.push(t('admin.promo.firstRechargeBonusDisplay', { amount: bonus.toFixed(2) }))
  }
  if (discount > 0) {
    const times = Number(code.first_recharge_discount_times ?? 1)
    const timesLabel =
      times === 0
        ? t('admin.promo.rechargeDiscountUnlimited')
        : t('admin.promo.rechargeDiscountTimesDisplay', { count: times })
    parts.push(t('admin.promo.firstRechargeDiscountDisplay', { rate: formatDiscountRate(discount), times: timesLabel }))
  }
  return parts.join(' / ')
}

const applyFirstRechargePromoPayload = (
  payload: {
    first_recharge_bonus_amount?: number | null
    first_recharge_discount_percent?: number | null
    first_recharge_discount_times?: number
    clear_first_recharge_bonus?: boolean
    clear_first_recharge_discount?: boolean
  },
  form: {
    first_recharge_bonus_amount: string | number
    first_recharge_discount_rate: string | number
    first_recharge_discount_times: string | number
  },
  existing?: PromoCode | null
): boolean => {
  const bonus = parseOptionalNumber(
    form.first_recharge_bonus_amount,
    0,
    Number.MAX_SAFE_INTEGER,
    'admin.promo.errorBadFirstRechargeBonus'
  )
  if (bonus === undefined) return false
  if (bonus === null) {
    if (existing?.first_recharge_bonus_amount != null) {
      payload.clear_first_recharge_bonus = true
    }
  } else {
    payload.first_recharge_bonus_amount = bonus
  }

  const discountRate = parseOptionalNumber(
    form.first_recharge_discount_rate,
    0.01,
    10,
    'admin.promo.errorBadFirstRechargeDiscount'
  )
  if (discountRate === undefined) return false
  if (discountRate === null) {
    if (existing?.first_recharge_discount_percent != null) {
      payload.clear_first_recharge_discount = true
    }
  } else {
    payload.first_recharge_discount_percent = Number((discountRate * 10).toFixed(2))
  }

  const discountTimes = parseOptionalNumber(
    form.first_recharge_discount_times,
    0,
    Number.MAX_SAFE_INTEGER,
    'admin.promo.errorBadRechargeDiscountTimes'
  )
  if (discountTimes === undefined) return false
  if (discountTimes !== null) {
    if (!Number.isInteger(discountTimes)) {
      appStore.showError(t('admin.promo.errorBadRechargeDiscountTimes'))
      return false
    }
    payload.first_recharge_discount_times = discountTimes
  }
  return true
}

// API calls
let abortController: AbortController | null = null

const loadCodes = async () => {
  if (abortController) {
    abortController.abort()
  }
  const currentController = new AbortController()
  abortController = currentController
  loading.value = true

  try {
    const response = await adminAPI.promo.list(
      pagination.page,
      pagination.page_size,
      {
        status: filters.status || undefined,
        search: searchQuery.value || undefined,
        sort_by: sortState.sort_by,
        sort_order: sortState.sort_order
      },
      { signal: currentController.signal }
    )
    if (currentController.signal.aborted || abortController !== currentController) return

    codes.value = response.items
    pagination.total = response.total
  } catch (error: any) {
    if (
      currentController.signal.aborted ||
      abortController !== currentController ||
      error?.name === 'AbortError' ||
      error?.code === 'ERR_CANCELED'
    ) {
      return
    }
    appStore.showError(t('admin.promo.failedToLoad'))
    console.error('Error loading promo codes:', error)
  } finally {
    if (abortController === currentController) {
      loading.value = false
      abortController = null
    }
  }
}

let searchTimeout: ReturnType<typeof setTimeout>
const handleSearch = () => {
  clearTimeout(searchTimeout)
  searchTimeout = setTimeout(() => {
    pagination.page = 1
    loadCodes()
  }, 300)
}

const handlePageChange = (page: number) => {
  pagination.page = page
  loadCodes()
}

const handlePageSizeChange = (pageSize: number) => {
  pagination.page_size = pageSize
  pagination.page = 1
  loadCodes()
}

const handleSort = (key: string, order: 'asc' | 'desc') => {
  sortState.sort_by = key
  sortState.sort_order = order
  pagination.page = 1
  loadCodes()
}

const copyToClipboard = async (text: string) => {
  const success = await clipboardCopy(text, t('admin.promo.copied'))
  if (success) {
    copiedCode.value = text
    setTimeout(() => {
      copiedCode.value = null
    }, 2000)
  }
}

// Create
const handleCreate = async () => {
  creating.value = true
  try {
    const payload: CreatePromoCodeRequest = {
      code: createForm.code || undefined,
      bonus_amount: createForm.bonus_amount,
      max_uses: createForm.max_uses,
      expires_at: createForm.expires_at_str ? Math.floor(new Date(createForm.expires_at_str).getTime() / 1000) : undefined,
      notes: createForm.notes || undefined
    }
    if (!applyFirstRechargePromoPayload(payload, createForm)) return
    await adminAPI.promo.create(payload)
    appStore.showSuccess(t('admin.promo.codeCreated'))
    showCreateDialog.value = false
    resetCreateForm()
    loadCodes()
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.promo.failedToCreate'))
  } finally {
    creating.value = false
  }
}

const resetCreateForm = () => {
  createForm.code = ''
  createForm.bonus_amount = 1
  createForm.first_recharge_bonus_amount = ''
  createForm.first_recharge_discount_rate = ''
  createForm.first_recharge_discount_times = 1
  createForm.max_uses = 0
  createForm.expires_at_str = ''
  createForm.notes = ''
}

// Edit
const handleEdit = (code: PromoCode) => {
  editingCode.value = code
  editForm.code = code.code
  editForm.bonus_amount = code.bonus_amount
  editForm.first_recharge_bonus_amount =
    code.first_recharge_bonus_amount != null ? String(code.first_recharge_bonus_amount) : ''
  editForm.first_recharge_discount_rate =
    code.first_recharge_discount_percent != null ? formatDiscountRate(code.first_recharge_discount_percent) : ''
  editForm.first_recharge_discount_times = code.first_recharge_discount_times ?? 1
  editForm.max_uses = code.max_uses
  editForm.status = code.status
  editForm.expires_at_str = code.expires_at ? new Date(code.expires_at).toISOString().slice(0, 16) : ''
  editForm.notes = code.notes || ''
  showEditDialog.value = true
}

const closeEditDialog = () => {
  showEditDialog.value = false
  editingCode.value = null
}

const handleUpdate = async () => {
  if (!editingCode.value) return

  updating.value = true
  try {
    const payload: UpdatePromoCodeRequest = {
      code: editForm.code,
      bonus_amount: editForm.bonus_amount,
      max_uses: editForm.max_uses,
      status: editForm.status,
      expires_at: editForm.expires_at_str ? Math.floor(new Date(editForm.expires_at_str).getTime() / 1000) : 0,
      notes: editForm.notes
    }
    if (!applyFirstRechargePromoPayload(payload, editForm, editingCode.value)) return
    await adminAPI.promo.update(editingCode.value.id, payload)
    appStore.showSuccess(t('admin.promo.codeUpdated'))
    closeEditDialog()
    loadCodes()
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.promo.failedToUpdate'))
  } finally {
    updating.value = false
  }
}

// Copy Register Link
const copyRegisterLink = async (code: PromoCode) => {
  const baseUrl = window.location.origin
  const registerLink = `${baseUrl}/register?promo=${encodeURIComponent(code.code)}`

  try {
    await navigator.clipboard.writeText(registerLink)
    appStore.showSuccess(t('admin.promo.registerLinkCopied'))
  } catch (error) {
    // Fallback for older browsers
    const textArea = document.createElement('textarea')
    textArea.value = registerLink
    document.body.appendChild(textArea)
    textArea.select()
    document.execCommand('copy')
    document.body.removeChild(textArea)
    appStore.showSuccess(t('admin.promo.registerLinkCopied'))
  }
}

// Delete
const handleDelete = (code: PromoCode) => {
  deletingCode.value = code
  showDeleteDialog.value = true
}

const confirmDelete = async () => {
  if (!deletingCode.value) return

  try {
    await adminAPI.promo.delete(deletingCode.value.id)
    appStore.showSuccess(t('admin.promo.codeDeleted'))
    showDeleteDialog.value = false
    deletingCode.value = null
    loadCodes()
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.promo.failedToDelete'))
  }
}

// View Usages
const handleViewUsages = async (code: PromoCode) => {
  currentViewingCode.value = code
  showUsagesDialog.value = true
  usagesPage.value = 1
  await loadUsages()
}

const loadUsages = async () => {
  if (!currentViewingCode.value) return
  usagesLoading.value = true
  usages.value = []

  try {
    const response = await adminAPI.promo.getUsages(
      currentViewingCode.value.id,
      usagesPage.value,
      usagesPageSize.value
    )
    usages.value = response.items
    usagesTotal.value = response.total
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.promo.failedToLoadUsages'))
  } finally {
    usagesLoading.value = false
  }
}

const handleUsagesPageChange = (page: number) => {
  usagesPage.value = page
  loadUsages()
}

onMounted(() => {
  loadCodes()
})

onUnmounted(() => {
  clearTimeout(searchTimeout)
  abortController?.abort()
})
</script>
