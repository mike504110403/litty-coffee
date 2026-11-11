import { http } from './http'

// 後台「串接憑證」：IG Meta App 與 Claude AI。DB 值優先於 .env，存檔立即生效，不用重啟。
export type CredentialKey = 'igAppId' | 'igAppSecret' | 'igVerifyToken' | 'anthropicApiKey' | 'aiModel' | 'aiEffort'
export type CredentialSource = 'db' | 'env' | 'none'

export interface CredentialField {
  set: boolean
  source: CredentialSource
  /** 尾 4 碼；值太短時為空字串 */
  hint: string
  /** 只有非 secret 欄位（App ID、模型、強度）才有全文 */
  value?: string
}

export type Credentials = Record<CredentialKey, CredentialField>

/** 只送要改的欄位；空字串＝不變；clear 列出要刪除後台設定（退回 .env）的欄位 */
export type CredentialsPatch = Partial<Record<CredentialKey, string>> & { clear?: CredentialKey[] }

export interface TestAiResult {
  ok: boolean
  error?: string
}

export const credentialsApi = {
  get: () => http.get<Credentials>('/settings/credentials').then((r) => r.data),
  update: (patch: CredentialsPatch) => http.put<Credentials>('/settings/credentials', patch).then((r) => r.data),
  /** 不帶 apiKey＝測目前生效的 key；帶了＝先測尚未儲存的 key（走 Models List，免費） */
  testAi: (apiKey?: string) =>
    http.post<TestAiResult>('/settings/credentials/test-ai', apiKey ? { apiKey } : {}).then((r) => r.data),
}
