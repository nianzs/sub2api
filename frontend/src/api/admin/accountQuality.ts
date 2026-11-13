/**
 * Admin account quality rules API endpoints.
 * Ported from ranxi2001/sub2api@f895ea743 (frontend/src/api/admin/accountQuality.ts).
 * Rules are scheduled test plans with a quality policy; create, update and
 * delete go through the scheduled test plan API.
 */

import { apiClient } from '../client'
import type { ScheduledTestPlan, ScheduledTestResult } from '@/types'

export async function listQualityPlans(): Promise<ScheduledTestPlan[]> {
  const { data } = await apiClient.get<ScheduledTestPlan[]>('/admin/account-quality-plans')
  return data ?? []
}

export async function runQualityPlan(id: number): Promise<void> {
  await apiClient.post(`/admin/account-quality-plans/${id}/run`)
}

// One quality round: the first result of the round plus counts across its samples.
export interface QualityOperation extends ScheduledTestResult {
  account_id: number
  account_name: string
  passed_count: number
  skipped_count?: number
  total_count: number
  result_ids: number[]
}

export async function listQualityOperations(
  beforeId = 0
): Promise<{ items: QualityOperation[]; next_cursor: number }> {
  const { data } = await apiClient.get<{ items?: QualityOperation[]; next_cursor?: number }>(
    '/admin/account-quality-results',
    { params: { before_id: beforeId } }
  )
  return { items: data?.items ?? [], next_cursor: data?.next_cursor ?? 0 }
}

export const accountQualityAPI = {
  listPlans: listQualityPlans,
  runPlan: runQualityPlan,
  listOperations: listQualityOperations
}

export default accountQualityAPI
