import { apiClient } from '../client'

export interface ZedAuthUrlResponse {
  auth_url: string
  session_id: string
}

export interface ZedCredentials {
  user_id?: string
  access_token?: string
  system_id?: string
  github_user_login?: string
  llm_token?: string
  expires_at?: string
  [key: string]: unknown
}

export async function generateAuthUrl(): Promise<ZedAuthUrlResponse> {
  const { data } = await apiClient.post<ZedAuthUrlResponse>('/admin/zed/oauth/auth-url')
  return data
}

export async function exchangeCode(payload: {
  session_id: string
  callback_url: string
  /** 必填：留空会建出一个所有推理都返回 trial_blocked (403) 的账号 */
  system_id: string
}): Promise<ZedCredentials> {
  const { data } = await apiClient.post<ZedCredentials>('/admin/zed/oauth/exchange-code', payload)
  return data
}

export async function importToken(payload: {
  user_id: string
  access_token: string
  /** 必填，同 exchangeCode */
  system_id: string
  github_user_login?: string
}): Promise<ZedCredentials> {
  const { data } = await apiClient.post<ZedCredentials>('/admin/zed/oauth/import-token', payload)
  return data
}

export default {
  generateAuthUrl,
  exchangeCode,
  importToken
}
