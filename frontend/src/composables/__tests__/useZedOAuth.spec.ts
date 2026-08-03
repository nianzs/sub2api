import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  generateAuthUrl: vi.fn(),
  exchangeCode: vi.fn(),
  importToken: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    zed: {
      generateAuthUrl: mocks.generateAuthUrl,
      exchangeCode: mocks.exchangeCode,
      importToken: mocks.importToken
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: mocks.showError
  })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

import { useZedOAuth } from '../useZedOAuth'

const SYSTEM_ID = '3f2504e0-4f89-11d3-9a0c-0305e82c3301'

describe('useZedOAuth', () => {
  beforeEach(() => {
    mocks.generateAuthUrl.mockReset()
    mocks.exchangeCode.mockReset()
    mocks.importToken.mockReset()
    mocks.showError.mockReset()
  })

  // An account stored without system_id mints tokens fine and then fails every
  // /completions request with trial_blocked (403), so the request must not even
  // be attempted.
  describe.each([
    ['exchangeCallback', 'exchangeCode'] as const,
    ['importToken', 'importToken'] as const
  ])('%s rejects a blank system_id', (method, apiName) => {
    it.each([['empty', ''], ['whitespace', '   ']])('%s', async (_label, systemId) => {
      const zed = useZedOAuth()

      const result =
        method === 'exchangeCallback'
          ? await zed.exchangeCallback({
              sessionId: 'session-1',
              callbackUrl: 'http://127.0.0.1:8765/?user_id=42&access_token=abc',
              systemId
            })
          : await zed.importToken({
              userId: '42',
              accessToken: 'long-lived',
              systemId
            })

      expect(result).toBeNull()
      expect(mocks[apiName]).not.toHaveBeenCalled()
      expect(zed.error.value).toBe('admin.accounts.oauth.zed.systemIdRequired')
      expect(mocks.showError).toHaveBeenCalledWith('admin.accounts.oauth.zed.systemIdRequired')
      expect(zed.loading.value).toBe(false)
    })
  })

  it('forwards a trimmed system_id when exchanging a callback', async () => {
    mocks.exchangeCode.mockResolvedValueOnce({ user_id: '42', system_id: SYSTEM_ID })
    const zed = useZedOAuth()

    const result = await zed.exchangeCallback({
      sessionId: 'session-1',
      callbackUrl: 'http://127.0.0.1:8765/?user_id=42&access_token=abc',
      systemId: `  ${SYSTEM_ID}  `
    })

    expect(result).toEqual({ user_id: '42', system_id: SYSTEM_ID })
    expect(mocks.exchangeCode).toHaveBeenCalledWith({
      session_id: 'session-1',
      callback_url: 'http://127.0.0.1:8765/?user_id=42&access_token=abc',
      system_id: SYSTEM_ID
    })
    expect(zed.loading.value).toBe(false)
  })

  it('forwards a trimmed system_id when importing a token', async () => {
    mocks.importToken.mockResolvedValueOnce({ user_id: '42', system_id: SYSTEM_ID })
    const zed = useZedOAuth()

    const result = await zed.importToken({
      userId: '42',
      accessToken: 'long-lived',
      systemId: `${SYSTEM_ID}\t`,
      githubLogin: 'octocat'
    })

    expect(result).toEqual({ user_id: '42', system_id: SYSTEM_ID })
    expect(mocks.importToken).toHaveBeenCalledWith({
      user_id: '42',
      access_token: 'long-lived',
      system_id: SYSTEM_ID,
      github_user_login: 'octocat'
    })
  })

  it('surfaces the backend detail when an exchange fails', async () => {
    mocks.exchangeCode.mockRejectedValueOnce({
      response: { data: { detail: 'zed sign-in session not found or expired' } }
    })
    const zed = useZedOAuth()

    const result = await zed.exchangeCallback({
      sessionId: 'stale-session',
      callbackUrl: 'http://127.0.0.1:8765/?user_id=42&access_token=abc',
      systemId: SYSTEM_ID
    })

    expect(result).toBeNull()
    expect(zed.error.value).toBe('zed sign-in session not found or expired')
    expect(zed.loading.value).toBe(false)
  })
})
