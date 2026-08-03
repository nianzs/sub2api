import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { ZedCredentials } from '@/api/admin/zed'

export function useZedOAuth() {
  const appStore = useAppStore()
  const { t } = useI18n()

  const authUrl = ref('')
  const sessionId = ref('')
  const loading = ref(false)
  const error = ref('')

  const resetState = () => {
    authUrl.value = ''
    sessionId.value = ''
    loading.value = false
    error.value = ''
  }

  const generateAuthUrl = async (): Promise<boolean> => {
    loading.value = true
    error.value = ''
    authUrl.value = ''
    sessionId.value = ''

    try {
      const response = await adminAPI.zed.generateAuthUrl()
      authUrl.value = response.auth_url
      sessionId.value = response.session_id
      return true
    } catch (err: any) {
      error.value = err.response?.data?.detail || t('admin.accounts.oauth.authFailed')
      appStore.showError(error.value)
      return false
    } finally {
      loading.value = false
    }
  }

  /**
   * system_id 缺失会让后端省略 x-zed-system-id：铸造照样成功，但每个推理请求都
   * 返回 trial_blocked (403)。在这里挡住，可以给出本地化提示，而不是让 gin 回一句
   * 中文 400。
   */
  const requireSystemId = (systemId: string): string | null => {
    const trimmed = systemId?.trim() || ''
    if (!trimmed) {
      error.value = t('admin.accounts.oauth.zed.systemIdRequired')
      appStore.showError(error.value)
      return null
    }
    return trimmed
  }

  const exchangeCallback = async (params: {
    sessionId: string
    callbackUrl: string
    systemId: string
  }): Promise<ZedCredentials | null> => {
    const systemId = requireSystemId(params.systemId)
    if (!systemId) return null

    loading.value = true
    error.value = ''
    try {
      return await adminAPI.zed.exchangeCode({
        session_id: params.sessionId,
        callback_url: params.callbackUrl,
        system_id: systemId
      })
    } catch (err: any) {
      error.value = err.response?.data?.detail || t('admin.accounts.oauth.authFailed')
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  const importToken = async (params: {
    userId: string
    accessToken: string
    systemId: string
    githubLogin?: string
  }): Promise<ZedCredentials | null> => {
    const systemId = requireSystemId(params.systemId)
    if (!systemId) return null

    loading.value = true
    error.value = ''
    try {
      return await adminAPI.zed.importToken({
        user_id: params.userId,
        access_token: params.accessToken,
        system_id: systemId,
        github_user_login: params.githubLogin || undefined
      })
    } catch (err: any) {
      error.value = err.response?.data?.detail || t('admin.accounts.oauth.authFailed')
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  const buildCredentials = (creds: ZedCredentials): Record<string, unknown> => ({
    user_id: creds.user_id,
    access_token: creds.access_token,
    system_id: creds.system_id,
    github_user_login: creds.github_user_login,
    llm_token: creds.llm_token,
    expires_at: creds.expires_at
  })

  return {
    authUrl,
    sessionId,
    loading,
    error,
    resetState,
    generateAuthUrl,
    exchangeCallback,
    importToken,
    buildCredentials
  }
}
