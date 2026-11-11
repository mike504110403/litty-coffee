<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Connection, CopyDocument, Key } from '@element-plus/icons-vue'
import { settingsApi, type SettingsInfo } from '@/api/settings'
import { useAuthStore } from '@/stores/auth'
import HelpTip from '@/components/HelpTip.vue'
import PillTabs from '@/components/PillTabs.vue'
import CredentialsPanel from '@/views/settings/CredentialsPanel.vue'
import IgAccountsPanel from '@/views/settings/IgAccountsPanel.vue'
import { useAccountsStore } from '@/stores/accounts'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
// 設定頁只剩 IG 帳號與憑證；外觀與密碼在頭像選單、管理員在獨立頁
const canIg = computed(() => auth.can('ig_accounts'))
const canCreds = computed(() => auth.can('credentials'))

type TabKey = 'ig' | 'credentials'
const sections = computed(() => [
  ...(canIg.value ? [{ key: 'ig' as TabKey, label: 'IG 帳號', icon: Connection }] : []),
  ...(canCreds.value ? [{ key: 'credentials' as TabKey, label: '憑證', icon: Key }] : []),
])
// 網址帶 ?tab=，重新整理停在同一區；值不合法或沒權限就退回第一個可用區塊
const tab = computed<TabKey>({
  get: () => {
    const q = route.query.tab
    return sections.value.find((x) => x.key === q)?.key ?? sections.value[0]?.key ?? 'ig'
  },
  set: (v) => router.replace({ query: { ...route.query, tab: v } }),
})

const info = ref<SettingsInfo | null>(null)
const accountsStore = useAccountsStore()

// 憑證存檔後 IG 卡片的「能否連結」要跟著更新
function onCredsSaved() {
  if (canIg.value) void load()
}

async function load() {
  info.value = await settingsApi.get()
}

onMounted(async () => {
  // OAuth callback 導回來時帶的結果
  const ok = route.query.ig === 'connected'
  const err = typeof route.query.ig_error === 'string' ? route.query.ig_error : ''
  if (ok || err) {
    const { ig: _ig, ig_error: _err, ...rest } = route.query
    router.replace({ query: rest })
    void accountsStore.fetch()
    if (ok) ElMessage.success('IG 帳號已連結')
    else ElMessage({ type: 'error', message: err, duration: 8000, showClose: true })
  }
  if (canIg.value) await load()
})

const integ = computed(() => info.value?.integration)

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已複製')
  } catch {
    ElMessage.warning('無法自動複製，請手動選取')
  }
}

const insecurePublic = computed(() => !!integ.value && !integ.value.publicBaseUrl.startsWith('https://'))
</script>

<template>
  <div class="settings">
    <h1 class="lc-page-title lc-page-title--solo">設定</h1>

    <el-empty v-if="!sections.length" description="沒有可設定的項目" />
    <div v-else class="layout">
      <!-- 桌機：左側子導覽；窄螢幕與手機：頂部分頁籤 -->
      <nav class="subnav" aria-label="設定項目">
        <button
          v-for="x in sections"
          :key="x.key"
          type="button"
          class="subnav-item"
          :class="{ on: tab === x.key }"
          :aria-current="tab === x.key ? 'page' : undefined"
          @click="tab = x.key"
        >
          <el-icon :size="18"><component :is="x.icon" /></el-icon>{{ x.label }}
        </button>
      </nav>
      <PillTabs v-if="sections.length > 1" v-model="tab" :tabs="sections" block class="subtabs" />

      <div class="pane">
        <template v-if="tab === 'ig'">
          <section v-loading="!info" class="lc-card block">
            <h2>IG 帳號</h2>
            <IgAccountsPanel v-if="info" :info="info" @reload="load" />
          </section>

          <section v-if="integ && info" class="lc-card block">
            <h2>
              串接資訊
              <HelpTip :width="340">
                設定 Meta 開發者後台時要填的值。使用 Instagram API with Unified Login（官方建議新整合採用的設定）：在 App
                的 Facebook 登入建立「配置」，勾選下列權限，把配置 ID 填進「憑證」。
              </HelpTip>
            </h2>
            <el-alert
              v-if="insecurePublic && !integ.simulator"
              type="warning"
              :closable="false"
              show-icon
              title="公開網址不是 HTTPS：IG 收不到 webhook、也抓不到要發佈的圖片"
              class="alert"
            />
            <dl class="facts copyable">
              <div>
                <dt>Webhook 網址</dt>
                <dd>
                  <code>{{ integ.webhookUrl }}</code>
                  <el-button link :icon="CopyDocument" aria-label="複製 Webhook 網址" @click="copy(integ.webhookUrl)" />
                </dd>
              </div>
              <div>
                <dt>OAuth 重新導向網址</dt>
                <dd>
                  <code>{{ integ.redirectUri }}</code>
                  <el-button link :icon="CopyDocument" aria-label="複製重新導向網址" @click="copy(integ.redirectUri)" />
                </dd>
              </div>
              <div>
                <dt>Webhook 驗證碼</dt>
                <dd class="muted">在「憑證」設定</dd>
              </div>
              <div>
                <dt>Webhook 欄位</dt>
                <dd class="wrap"><code>messages</code><code>messaging_seen</code><code>message_reactions</code></dd>
              </div>
              <div>
                <dt>配置要勾的權限</dt>
                <dd class="wrap">
                  <code v-for="p in integ.permissions" :key="p">{{ p }}</code>
                </dd>
              </div>
              <div>
                <dt>Graph API</dt>
                <dd><code>{{ integ.apiBase || '—' }}</code></dd>
              </div>
              <div>
                <dt>店家時區</dt>
                <dd>{{ info.timezone }}</dd>
              </div>
            </dl>
          </section>
        </template>

        <CredentialsPanel v-else @saved="onCredsSaved" />
      </div>
    </div>
  </div>
</template>

<style scoped lang="scss">
.layout {
  display: grid;
  grid-template-columns: 200px minmax(0, 960px);
  gap: 24px;
  align-items: start;
}
.subnav {
  position: sticky;
  top: 28px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.subnav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  border: 0;
  background: transparent;
  font: inherit;
  font-size: 15px;
  color: var(--lc-muted);
  padding: 10px 14px;
  border-radius: 12px;
  cursor: pointer;
  text-align: left;
  &:hover {
    background: var(--lc-bg-hover);
    color: var(--lc-text);
  }
  &.on {
    background: var(--lc-bg-tab-on);
    color: var(--lc-text);
    font-weight: 500;
    box-shadow: var(--lc-tab-shadow);
  }
}
.subtabs {
  display: none;
}
.pane {
  min-width: 0;
}
.block {
  padding: 20px;
  margin-bottom: 16px;
  min-height: 80px;
  h2 {
    margin: 0 0 14px;
    font-size: 16px;
    font-weight: 600;
  }
}
.alert {
  margin-bottom: 14px;
}
.facts {
  margin: 0;
  > div {
    display: grid;
    grid-template-columns: 160px 1fr;
    gap: 12px;
    padding: 10px 0;
    border-top: 1px solid var(--lc-border);
    &:first-child {
      border-top: 0;
    }
  }
  dt {
    color: var(--lc-muted);
    font-size: 13px;
    padding-top: 2px;
  }
  dd {
    margin: 0;
    font-size: 14px;
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    min-width: 0;
  }
  code {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 12.5px;
    background: var(--lc-cream);
    padding: 2px 6px;
    border-radius: 6px;
    word-break: break-all;
  }
}
.copyable dd {
  flex-wrap: nowrap;
  align-items: flex-start;
  code {
    flex: 0 1 auto;
    min-width: 0;
  }
  .el-button {
    flex: none;
  }
}
.copyable dd.wrap {
  flex-wrap: wrap;
}
.muted {
  color: var(--lc-muted);
  font-size: 13px;
}
@media (max-width: 1023px) {
  .layout {
    grid-template-columns: minmax(0, 1fr);
    gap: 16px;
  }
  .subnav {
    display: none;
  }
  .subtabs {
    display: flex;
  }
}
@media (max-width: 768px) {
  .settings .lc-page-title {
    display: none;
  }
  .block {
    padding: 16px;
  }
  .facts > div {
    grid-template-columns: 1fr;
    gap: 4px;
  }
}
</style>
