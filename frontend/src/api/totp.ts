import { http } from './http'

// Google Authenticator（TOTP）綁定：產生 secret／QR code → 輸入一組碼確認 → 拿到 8 組備援碼（只顯示這一次）
export interface TotpSetup {
  secret: string // 無法掃描時手動輸入
  otpauthUrl: string
  qrCode: string // PNG data URL
}

export const totpApi = {
  setup: () => http.post<TotpSetup>('/auth/totp/setup').then((r) => r.data),
  enable: (code: string) => http.post<{ recoveryCodes: string[] }>('/auth/totp/enable', { code }).then((r) => r.data.recoveryCodes),
}
