import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { accountsApi, accountLabel, type IgAccount } from '@/api/accounts'

const LAST_KEY = 'lc-last-account'

// 登入者看得到的 IG 帳號；各頁的帳號篩選、選擇器共用
export const useAccountsStore = defineStore('accounts', () => {
  const list = ref<IgAccount[]>([])
  const loaded = ref(false)
  let pending: Promise<void> | null = null

  async function fetch() {
    list.value = await accountsApi.list()
    loaded.value = true
  }

  // 只抓一次；設定頁連結／停用後用 fetch() 重抓
  function ensure(): Promise<void> {
    if (loaded.value) return Promise.resolve()
    pending ??= fetch().finally(() => {
      pending = null
    })
    return pending
  }

  const byId = computed(() => new Map(list.value.map((a) => [a.id, a])))
  const multi = computed(() => list.value.length > 1)

  function label(id: number | null | undefined): string {
    const a = id == null ? undefined : byId.value.get(id)
    return a ? accountLabel(a) : '未知帳號'
  }

  // 上次在排程／自動回覆選的帳號（每台瀏覽器各自記）
  function lastUsed(): number | null {
    let id: number | null = null
    try {
      id = Number(localStorage.getItem(LAST_KEY)) || null
    } catch {
      /* 無痕模式等 */
    }
    if (id != null && byId.value.has(id)) return id
    return list.value.find((a) => a.active)?.id ?? list.value[0]?.id ?? null
  }

  function remember(id: number | null) {
    if (id == null) return
    try {
      localStorage.setItem(LAST_KEY, String(id))
    } catch {
      /* ignore */
    }
  }

  function reset() {
    list.value = []
    loaded.value = false
  }

  return { list, loaded, byId, multi, fetch, ensure, label, lastUsed, remember, reset }
})
