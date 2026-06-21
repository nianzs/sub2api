/**
 * Admin Daily Check-in API endpoints
 * Lists daily reward records granted to users.
 */

import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'

export interface DailyCheckinRecord {
  user_id: number
  email: string
  username: string
  checkin_date: string
  reward: number
  created_at: string
}

export interface ListDailyCheckinRecordsParams {
  page?: number
  page_size?: number
  search?: string
  user_id?: number
  start_date?: string
  end_date?: string
  sort_by?: string
  sort_order?: 'asc' | 'desc'
}

export interface DailyCheckinSettings {
  enabled: boolean
  daily_total_limit: number
  min_reward: number
  max_reward: number
  min_recharge_amount: number
}

export async function listRecords(
  params: ListDailyCheckinRecordsParams = {},
): Promise<PaginatedResponse<DailyCheckinRecord>> {
  const { data } = await apiClient.get<PaginatedResponse<DailyCheckinRecord>>(
    '/admin/daily-checkins',
    {
      params: {
        page: params.page ?? 1,
        page_size: params.page_size ?? 20,
        search: params.search ?? '',
        user_id: params.user_id || undefined,
        start_date: params.start_date || undefined,
        end_date: params.end_date || undefined,
        sort_by: params.sort_by || undefined,
        sort_order: params.sort_order || undefined,
      },
    },
  )
  return data
}

export async function getSettings(): Promise<DailyCheckinSettings> {
  const { data } = await apiClient.get<DailyCheckinSettings>('/admin/daily-checkins/settings')
  return data
}

export async function updateSettings(settings: DailyCheckinSettings): Promise<DailyCheckinSettings> {
  const { data } = await apiClient.put<DailyCheckinSettings>('/admin/daily-checkins/settings', settings)
  return data
}

export const dailyCheckinsAPI = {
  listRecords,
  getSettings,
  updateSettings,
}

export default dailyCheckinsAPI
