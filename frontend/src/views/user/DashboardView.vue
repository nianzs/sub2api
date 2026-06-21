<template>
  <AppLayout>
    <div class="space-y-6">
      <div v-if="loading" class="flex items-center justify-center py-12"><LoadingSpinner /></div>
      <template v-else-if="stats">
        <div v-if="dailyCheckinStatus?.enabled" class="flex justify-end">
          <button
            type="button"
            class="btn btn-primary inline-flex min-w-[8rem] items-center justify-center gap-2"
            :disabled="dailyCheckinDisabled"
            :title="dailyCheckinTitle"
            @click="handleDailyCheckin"
          >
            <Icon :name="dailyCheckinStatus.checked_in_today ? 'checkCircle' : 'gift'" size="sm" />
            <span>{{ dailyCheckinButtonText }}</span>
          </button>
        </div>
        <UserDashboardStats :stats="stats" :balance="user?.balance || 0" :is-simple="authStore.isSimpleMode" :platform-quotas="platformQuotas" />
        <UserDashboardCharts v-model:startDate="startDate" v-model:endDate="endDate" v-model:granularity="granularity" :loading="loadingCharts" :trend="trendData" :models="modelStats" @dateRangeChange="loadCharts" @granularityChange="loadCharts" @refresh="refreshAll" />
        <div class="grid grid-cols-1 gap-6 lg:grid-cols-3">
          <div class="lg:col-span-2"><UserDashboardRecentUsage :data="recentUsage" :loading="loadingUsage" /></div>
          <div class="lg:col-span-1"><UserDashboardQuickActions /></div>
        </div>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'; import { useI18n } from 'vue-i18n'; import { useAuthStore } from '@/stores/auth'; import { useAppStore } from '@/stores'; import { usageAPI, type UserDashboardStats as UserStatsType } from '@/api/usage'
import AppLayout from '@/components/layout/AppLayout.vue'; import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import UserDashboardStats from '@/components/user/dashboard/UserDashboardStats.vue'; import UserDashboardCharts from '@/components/user/dashboard/UserDashboardCharts.vue'
import UserDashboardRecentUsage from '@/components/user/dashboard/UserDashboardRecentUsage.vue'; import UserDashboardQuickActions from '@/components/user/dashboard/UserDashboardQuickActions.vue'
import Icon from '@/components/icons/Icon.vue'
import type { UsageLog, TrendDataPoint, ModelStat, PlatformQuotaItem, DailyCheckinStatus } from '@/types'
import { getMyPlatformQuotas, getDailyCheckinStatus, claimDailyCheckin } from '@/api/user'
import { extractI18nErrorMessage } from '@/utils/apiError'

const { t } = useI18n(); const authStore = useAuthStore(); const appStore = useAppStore(); const user = computed(() => authStore.user)
const stats = ref<UserStatsType | null>(null); const loading = ref(false); const loadingUsage = ref(false); const loadingCharts = ref(false)
const trendData = ref<TrendDataPoint[]>([]); const modelStats = ref<ModelStat[]>([]); const recentUsage = ref<UsageLog[]>([])
const platformQuotas = ref<PlatformQuotaItem[] | null>(null)
const dailyCheckinStatus = ref<DailyCheckinStatus | null>(null); const dailyCheckinLoading = ref(false)

const formatLD = (d: Date) => d.toISOString().split('T')[0]
const startDate = ref(formatLD(new Date(Date.now() - 6 * 86400000))); const endDate = ref(formatLD(new Date())); const granularity = ref('day')

const loadStats = async () => { loading.value = true; try { await authStore.refreshUser(); stats.value = await usageAPI.getDashboardStats() } catch (error) { console.error('Failed to load dashboard stats:', error) } finally { loading.value = false } }
const loadCharts = async () => { loadingCharts.value = true; try { const res = await Promise.all([usageAPI.getDashboardTrend({ start_date: startDate.value, end_date: endDate.value, granularity: granularity.value as any }), usageAPI.getDashboardModels({ start_date: startDate.value, end_date: endDate.value })]); trendData.value = res[0].trend || []; modelStats.value = res[1].models || [] } catch (error) { console.error('Failed to load charts:', error) } finally { loadingCharts.value = false } }
const loadRecent = async () => { loadingUsage.value = true; try { const res = await usageAPI.getByDateRange(startDate.value, endDate.value); recentUsage.value = res.items.slice(0, 5) } catch (error) { console.error('Failed to load recent usage:', error) } finally { loadingUsage.value = false } }
const loadPlatformQuotas = async () => { try { const data = await getMyPlatformQuotas(); platformQuotas.value = data.platform_quotas ?? [] } catch (error) { console.warn('Failed to load platform quotas:', error); platformQuotas.value = [] } }
const loadDailyCheckin = async () => { try { dailyCheckinStatus.value = await getDailyCheckinStatus() } catch (error) { console.warn('Failed to load daily check-in status:', error); dailyCheckinStatus.value = null } }
const dailyCheckinDisabled = computed(() => dailyCheckinLoading.value || !dailyCheckinStatus.value?.enabled || !dailyCheckinStatus.value.recharge_eligible || dailyCheckinStatus.value.checked_in_today || dailyCheckinStatus.value.exhausted_today)
const dailyCheckinButtonText = computed(() => {
  if (dailyCheckinLoading.value) return t('dashboard.dailyCheckin.checking')
  if (dailyCheckinStatus.value?.checked_in_today) return t('dashboard.dailyCheckin.checked')
  if (dailyCheckinStatus.value?.exhausted_today) return t('dashboard.dailyCheckin.exhausted')
  return t('dashboard.dailyCheckin.action')
})
const dailyCheckinTitle = computed(() => {
  const status = dailyCheckinStatus.value
  if (!status) return ''
  if (status.checked_in_today) return t('dashboard.dailyCheckin.checkedHint', { amount: formatCurrency(status.today_reward) })
  if (status.exhausted_today) return t('dashboard.dailyCheckin.exhaustedHint')
  if (!status.recharge_eligible) return t('dashboard.dailyCheckin.rechargeRequiredHint', { amount: formatCurrency(status.min_recharge_amount), current: formatCurrency(status.total_recharged) })
  return t('dashboard.dailyCheckin.hint', { min: formatCurrency(status.min_reward), max: formatCurrency(status.max_reward) })
})
const formatCurrency = (value: number) => `$${Number(value || 0).toFixed(2)}`
const refreshAll = () => { loadStats(); loadCharts(); loadRecent(); loadPlatformQuotas(); loadDailyCheckin() }
const handleDailyCheckin = async () => {
  if (dailyCheckinDisabled.value) return
  dailyCheckinLoading.value = true
  try {
    const result = await claimDailyCheckin()
    dailyCheckinStatus.value = result
    appStore.showSuccess(t('dashboard.dailyCheckin.success', { amount: formatCurrency(result.reward) }))
    await authStore.refreshUser()
  } catch (error) {
    appStore.showError(extractI18nErrorMessage(error, t, 'dashboard.dailyCheckin.errors', t('dashboard.dailyCheckin.failed')))
    await loadDailyCheckin()
  } finally {
    dailyCheckinLoading.value = false
  }
}

onMounted(() => { refreshAll() })
</script>
