<script setup lang="ts">
import HelpTip from '@/components/HelpTip.vue'
import { computed, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { settingsApi, type IgAccount, type IgConnection, type SettingsInfo } from '@/api/settings'
import { accountLabel } from '@/api/accounts'
import AccountAvatar from '@/components/AccountAvatar.vue'
import { useAccountsStore } from '@/stores/accounts'

// 設定頁的 IG 帳號區塊：依連線分組列出帳號；連線層級（連結／同步／斷開）只給可見全部帳號的人
const props = defineProps<{ info: SettingsInfo }>()
const emit = defineEmits<{ reload: [] }>()
const accountsStore = useAccountsStore()

const integ = computed(() => props.info.integration)
const groups = computed(() =>
  props.info.connections.map((c) => ({ conn: c, accounts: props.info.accounts.filter((a) => a.connectionId === c.id) })),
)
// 沒掛在任何（看得到的）連線上的帳號：斷開過、授權沒包含、或只能看部分帳號的人看到的全部
const loose = computed(() =>
  props.info.accounts.filter((a) => !props.info.connections.some((c) => c.id === a.connectionId)),
)

const busy = ref('')
const DAY = 24 * 3600 * 1000
const fmtDate = (iso: string | null) =>
  iso ? new Date(iso).toLocaleDateString('zh-TW', { year: 'numeric', month: 'long', day: 'numeric' }) : '—'

function expiry(c: IgConnection): { type: 'success' | 'warning' | 'danger'; text: string } {
  if (!c.tokenExpires) return { type: 'success', text: '永不過期' }
  const d = Math.floor((new Date(c.tokenExpires).getTime() - Date.now()) / DAY)
  if (d < 0) return { type: 'danger', text: '已過期，需重新連結' }
  if (c.needsRelink) return { type: 'warning', text: `剩 ${d} 天，需重新連結` }
  return { type: 'success', text: `${fmtDate(c.tokenExpires)} 到期` }
}

async function after(fn: () => Promise<unknown>, key: string, okMsg: string) {
  busy.value = key
  try {
    await fn()
    ElMessage.success(okMsg)
  } catch {
    // 錯誤已由攔截器顯示；失敗原因也寫在 lastError
  } finally {
    busy.value = ''
    emit('reload')
    void accountsStore.fetch()
  }
}

function connect() {
  window.location.href = settingsApi.connectUrl
}

const sync = (c: IgConnection) =>
  after(() => settingsApi.refreshConnection(c.id), `sync${c.id}`, '已同步帳號清單')

// Unified Login 的指派清單可能是空的（實測如此），讓店長輸入 IG 帳號編號手動加入
async function addManually(c: IgConnection) {
  let id: string
  try {
    const r = await ElMessageBox.prompt(
      '到 Meta 商家設定 →「帳號」→「Instagram 帳號」，點帳號後會看到一串數字的「編號」。後台會先確認這個授權讀得到它才加入。',
      '手動加入 IG 帳號',
      {
        confirmButtonText: '加入',
        cancelButtonText: '取消',
        inputPlaceholder: '例如 17841467271924092',
        inputPattern: /^\s*\d+\s*$/,
        inputErrorMessage: 'IG 帳號編號是一串數字',
      },
    )
    id = r.value.trim()
  } catch {
    return
  }
  await after(() => settingsApi.addAccount(c.id, id), `add${c.id}`, '已加入帳號')
}

async function disconnect(c: IgConnection, accounts: IgAccount[]) {
  try {
    await ElMessageBox.confirm(
      `底下 ${accounts.length} 個帳號會收不到新私訊、自動回覆和排程發文都會停止。已收到的對話與顧客資料會保留，之後重新連結即可恢復。`,
      '斷開這個連線？',
      { confirmButtonText: '斷開連線', cancelButtonText: '取消', type: 'warning', confirmButtonClass: 'el-button--danger' },
    )
  } catch {
    return
  }
  await after(() => settingsApi.disconnect(c.id), `disc${c.id}`, '已斷開')
}

async function toggle(a: IgAccount) {
  if (a.active) {
    try {
      await ElMessageBox.confirm(
        '停用後這個帳號的私訊不再收進來、也不會自動回覆或發文；資料保留，隨時可以再啟用。',
        `停用 ${accountLabel(a)}？`,
        { confirmButtonText: '停用', cancelButtonText: '取消', type: 'warning' },
      )
    } catch {
      return
    }
  }
  await after(() => settingsApi.setActive(a.id, !a.active), `act${a.id}`, a.active ? '已停用' : '已啟用')
}

const resubscribe = (a: IgAccount) => after(() => settingsApi.resubscribe(a.id), `sub${a.id}`, '已訂閱私訊通知')
</script>

<template>
  <div class="ig-panel">
    <template v-for="g in groups" :key="g.conn.id">
      <div class="conn">
        <div class="conn-head">
          <div class="conn-title">
            <strong>業務組合 {{ g.conn.clientBusinessId || '—' }}</strong>
            <el-tag size="small" :type="expiry(g.conn).type" round>{{ expiry(g.conn).text }}</el-tag>
          </div>
          <div class="conn-sub">連結於 {{ fmtDate(g.conn.connectedAt) }} · {{ g.accounts.length }} 個帳號</div>
        </div>
        <el-alert v-if="g.conn.lastError" type="error" :closable="false" show-icon :title="g.conn.lastError" class="alert" />
        <el-alert
          v-else-if="g.conn.needsRelink"
          type="warning"
          :closable="false"
          show-icon
          title="授權快到期：請按「重新連結」，否則到期後收發私訊和發文都會失敗"
          class="alert"
        />
        <div class="rows">
          <div v-for="a in g.accounts" :key="a.id" class="row">
            <AccountAvatar :account="a" :size="40" />
            <div class="names">
              <div class="name">{{ a.name || accountLabel(a) }}</div>
              <div class="handle">
                {{ accountLabel(a) }}
                <el-tag v-if="!a.active" size="small" type="info" round>已停用</el-tag>
                <el-tag v-else size="small" :type="a.webhookSubscribed ? 'success' : 'danger'" round>
                  {{ a.webhookSubscribed ? '私訊通知已訂閱' : '私訊通知未訂閱' }}
                </el-tag>
              </div>
              <div v-if="a.lastError" class="err">{{ a.lastError }}</div>
            </div>
            <div class="row-actions">
              <el-button
                v-if="!a.webhookSubscribed && a.active"
                size="small"
                :loading="busy === `sub${a.id}`"
                @click="resubscribe(a)"
              >
                重試訂閱
              </el-button>
              <el-button size="small" :loading="busy === `act${a.id}`" @click="toggle(a)">
                {{ a.active ? '停用' : '啟用' }}
              </el-button>
            </div>
          </div>
        </div>
        <div class="actions">
          <el-button size="small" :loading="busy === `sync${g.conn.id}`" @click="sync(g.conn)">同步帳號</el-button>
          <el-button size="small" :loading="busy === `add${g.conn.id}`" @click="addManually(g.conn)">手動加入帳號</el-button>
          <el-button size="small" @click="connect">重新連結</el-button>
          <el-button
            size="small"
            type="danger"
            plain
            :loading="busy === `disc${g.conn.id}`"
            @click="disconnect(g.conn, g.accounts)"
          >
            斷開連線
          </el-button>
        </div>
      </div>
    </template>

    <div v-if="loose.length" class="conn">
      <div v-if="info.manageConnections" class="conn-head">
        <div class="conn-title">
          <strong>未連結的帳號</strong>
          <HelpTip text="對話與顧客資料都保留；重新連結（授權時勾選這些帳號）即可恢復收發。" />
        </div>
      </div>
      <div class="rows">
        <div v-for="a in loose" :key="a.id" class="row">
          <AccountAvatar :account="a" :size="40" />
          <div class="names">
            <div class="name">{{ a.name || accountLabel(a) }}</div>
            <div class="handle">
              {{ accountLabel(a) }}
              <el-tag v-if="!a.active" size="small" type="info" round>已停用</el-tag>
              <el-tag v-if="!a.connected" size="small" type="warning" round>需重新連結</el-tag>
              <el-tag v-else size="small" :type="a.webhookSubscribed ? 'success' : 'danger'" round>
                {{ a.webhookSubscribed ? '私訊通知已訂閱' : '私訊通知未訂閱' }}
              </el-tag>
            </div>
            <div v-if="a.lastError" class="err">{{ a.lastError }}</div>
          </div>
          <div class="row-actions">
            <el-button
              v-if="a.connected && !a.webhookSubscribed && a.active"
              size="small"
              :loading="busy === `sub${a.id}`"
              @click="resubscribe(a)"
            >
              重試訂閱
            </el-button>
            <el-button size="small" :loading="busy === `act${a.id}`" @click="toggle(a)">
              {{ a.active ? '停用' : '啟用' }}
            </el-button>
          </div>
        </div>
      </div>
    </div>

    <p v-if="!info.accounts.length && !info.manageConnections" class="muted">你目前沒有可見的 IG 帳號。</p>

    <div v-if="info.manageConnections" class="connect">
      <el-alert
        v-if="!integ.oauthReady"
        type="warning"
        :closable="false"
        show-icon
        title="尚未設定 Meta App ID／App Secret／配置 ID"
        description="到下方「憑證」填寫（或在後端 .env 設 IG_APP_ID／IG_APP_SECRET／IG_CONFIG_ID）。"
        class="alert"
      />
      <el-alert
        v-else-if="integ.simulator"
        type="info"
        :closable="false"
        show-icon
        title="模擬模式：不會真的跳到 Facebook，會直接連上兩個模擬商家帳號"
        class="alert"
      />
      <el-alert
        v-if="integ.envTokenSet"
        type="info"
        :closable="false"
        show-icon
        title="沒有連線的帳號會暫用後端 .env 的 IG_ACCESS_TOKEN；連結後改用連線的授權"
        class="alert"
      />
      <p v-if="!info.accounts.length" class="empty-line">還沒有連結任何 IG 帳號</p>
      <div class="connect-row">
        <el-button type="primary" :disabled="!integ.oauthReady" @click="connect">
          {{ info.connections.length ? '連結其他 IG 帳號' : '連結 IG 帳號' }}
        </el-button>
        <HelpTip :width="340">
          連結後才能收發私訊、自動回覆和排程發文；按下按鈕會轉到 Facebook 登入並授權，完成後自動回到這裡。
          一次授權可連多個 IG 專業帳號（授權畫面勾選要連的帳號），並同步這個授權看得到的全部帳號；之後在 Meta
          加減帳號，按「同步帳號」即可。帳號要是「商家」或「創作者」帳號，並已連到業務組合。
        </HelpTip>
      </div>
    </div>
  </div>
</template>

<style scoped lang="scss">
.ig-panel {
  // 多個業務組合時桌機並排；只有一個就撐滿
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(400px, 1fr));
  gap: 14px;
  align-items: start;
}
.connect,
.muted {
  grid-column: 1 / -1;
}
.conn {
  border: 1px solid var(--lc-border);
  border-radius: 14px;
  padding: 14px;
}
.conn-title {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.conn-sub {
  margin-top: 2px;
  font-size: 12px;
  color: var(--lc-muted);
}
.conn-head {
  margin-bottom: 10px;
}
.alert {
  margin-bottom: 10px;
}
.rows {
  display: flex;
  flex-direction: column;
}
.row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 0;
  border-top: 1px solid var(--lc-border);
  &:first-child {
    border-top: 0;
  }
}
.names {
  flex: 1;
  min-width: 0;
}
.name {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.handle {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 2px;
  font-size: 13px;
  color: var(--lc-muted);
}
.err {
  margin-top: 4px;
  font-size: 12px;
  color: var(--lc-danger);
  word-break: break-word;
}
.row-actions,
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  .el-button + .el-button {
    margin-left: 0;
  }
}
.row-actions {
  flex-shrink: 0;
}
.actions {
  margin-top: 10px;
}
.empty-line {
  margin: 0 0 10px;
  font-size: 14px;
  color: var(--lc-muted);
}
.connect-row {
  display: flex;
  align-items: center;
  gap: 4px;
}
.muted {
  color: var(--lc-muted);
  font-size: 13px;
}
@media (max-width: 480px) {
  .ig-panel {
    grid-template-columns: minmax(0, 1fr);
  }
}
@media (max-width: 768px) {
  .row {
    flex-wrap: wrap;
  }
  .row-actions {
    width: 100%;
    justify-content: flex-end;
  }
}
</style>
