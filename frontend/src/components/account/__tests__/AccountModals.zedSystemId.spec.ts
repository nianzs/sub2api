import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const read = (file: string) =>
  readFileSync(resolve(process.cwd(), 'src/components/account', file), 'utf8')

const createSource = read('CreateAccountModal.vue')
const editSource = read('EditAccountModal.vue')

// system_id is required because the upstream ties plan and trial eligibility to
// it: a missing value makes the backend omit x-zed-system-id, which mints fine
// and then returns trial_blocked (403) on every /completions request. The i18n
// hint has always claimed "Required", so these assertions are what make that
// claim true.
describe('CreateAccountModal Zed system_id', () => {
  it('gates the exchange button on a non-empty system_id', () => {
    expect(createSource).toContain("if (form.platform === 'zed') {")
    expect(createSource).toContain('zedSystemId.value.trim() &&')
  })

  it('sends a trimmed system_id instead of coercing blanks to undefined', () => {
    expect(createSource).toContain('systemId: zedSystemId.value.trim()')
    expect(createSource).not.toContain('systemId: zedSystemId.value.trim() || undefined')
  })

  it('guards handleZedExchange so a blank value reports a localized error', () => {
    expect(createSource).toContain("zedOAuth.error.value = t('admin.accounts.oauth.zed.systemIdRequired')")
  })

  it('marks the input required and explains a blank or oddly shaped value', () => {
    expect(createSource).toContain('data-testid="zed-system-id-input"')
    expect(createSource).toContain("t('admin.accounts.oauth.zed.systemIdRequired')")
    expect(createSource).toContain("t('admin.accounts.oauth.zed.systemIdFormatWarning')")
    expect(createSource).toContain('isUuidLikeSystemId(zedSystemId)')
  })
})

// Without an edit path, a typo'd system_id could only be fixed by deleting and
// recreating the account.
describe('EditAccountModal Zed system_id', () => {
  it('renders the field for zed oauth accounts', () => {
    expect(editSource).toContain("account.platform === 'zed' && account.type === 'oauth'")
    expect(editSource).toContain('data-testid="zed-system-id-input"')
  })

  it('hydrates the current value from credentials', () => {
    expect(editSource).toContain("typeof credentials?.system_id === 'string'")
  })

  it('blocks the save on a blank value rather than persisting a broken account', () => {
    expect(editSource).toContain('applyZedSystemID(newCredentials, zedSystemId.value)')
    expect(editSource).toContain("appStore.showError(t('admin.accounts.oauth.zed.systemIdRequired'))")
  })
})
