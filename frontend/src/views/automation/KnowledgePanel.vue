<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Delete } from '@element-plus/icons-vue'
import { automationApi, type Knowledge } from '@/api/automation'
import { kindLabels, postThumb, postsApi, type Post } from '@/api/posts'
import HelpTip from '@/components/HelpTip.vue'
import DistillHint from './DistillHint.vue'

// 編輯的是這個 IG 帳號的資料（父層切換帳號時以 key 重建本元件）
const props = defineProps<{ accountId: number; distillPending?: number }>()
const emit = defineEmits<{ 'go-distill': [] }>()

const items = ref<Knowledge[]>([])
const loading = ref(true)
const dirty = ref<Set<number>>(new Set())
const saving = ref<number | null>(null)

const hints: Record<string, string> = {
  營業時間: '例：週二～週日 10:00–19:00，週一公休；國定假日照常營業。',
  地址與交通: '例：台北市大安區 OO 路 12 號 1 樓；捷運忠孝復興站 3 號出口步行 5 分鐘；附近有收費停車場。',
  菜單與價格: '例：美式 90／拿鐵 120／燕麥拿鐵 +20；手工司康 75；低消一杯飲品。',
  訂位方式: '例：4 人以下不開放訂位，現場候位；5 人以上請私訊告知日期時間人數，由店員確認。',
  常見問題: '例：有插座與 Wi-Fi；不限時但週末限 2 小時；可帶寵物（需推車或牽繩）。',
}

async function load() {
  loading.value = true
  try {
    items.value = await automationApi.knowledge(props.accountId)
    dirty.value = new Set()
  } finally {
    loading.value = false
  }
}
onMounted(load)

// ---- IG 貼文（自動同步）：帳號層級開關＋最近 30 篇已發佈貼文，每篇可單獨排除 ----
const postsKb = ref(true)
const postsKbSaving = ref(false)
const igPosts = ref<Post[]>([])
const igLoading = ref(true)
const togglingPost = ref<number | null>(null)

async function loadIgPosts() {
  igLoading.value = true
  try {
    const [s, list] = await Promise.all([
      automationApi.settings(props.accountId),
      postsApi.list({ status: 'published', account: props.accountId }),
    ])
    postsKb.value = s.postsKbEnabled ?? true // 後端尚未回這個欄位時視為預設（開）
    igPosts.value = list
      .sort((a, b) => (b.publishedAt ?? b.scheduledAt ?? '').localeCompare(a.publishedAt ?? a.scheduledAt ?? ''))
      .slice(0, 30)
  } catch {
    igPosts.value = []
  } finally {
    igLoading.value = false
  }
}
onMounted(loadIgPosts)

async function setPostsKb(v: boolean) {
  postsKbSaving.value = true
  try {
    const s = await automationApi.patchSettings(props.accountId, { postsKbEnabled: v })
    postsKb.value = s.postsKbEnabled ?? v
  } catch {
    postsKb.value = !v // 失敗就還原；錯誤訊息已由 http 攔截器顯示
  } finally {
    postsKbSaving.value = false
  }
}

async function setPostRef(p: Post, on: boolean) {
  togglingPost.value = p.id
  try {
    const np = await postsApi.setAiExclude(p.id, !on)
    igPosts.value = igPosts.value.map((x) => (x.id === np.id ? np : x))
  } catch {
    // 失敗：開關維持原狀（由 :model-value 綁 aiExclude）
  } finally {
    togglingPost.value = null
  }
}

const shortDate = (iso: string | null) => {
  if (!iso) return ''
  const d = new Date(iso)
  return `${d.getMonth() + 1}/${d.getDate()}`
}

const markDirty = (k: Knowledge) => dirty.value.add(k.id)

async function save(k: Knowledge) {
  saving.value = k.id
  try {
    Object.assign(k, await automationApi.saveKnowledge(props.accountId, k))
    dirty.value.delete(k.id)
    ElMessage.success(`「${k.title}」已儲存`)
  } finally {
    saving.value = null
  }
}

async function add() {
  const res = await ElMessageBox.prompt('新段落的標題', '新增段落', {
    confirmButtonText: '新增',
    cancelButtonText: '取消',
    inputPlaceholder: '例如：甜點、包場、寵物政策',
    inputValidator: (v) => !!v?.trim() || '請輸入標題',
  }).catch(() => null)
  if (!res) return
  const value = res.value
  const sort = (items.value[items.value.length - 1]?.sort ?? 0) + 10
  items.value.push(await automationApi.saveKnowledge(props.accountId, { title: value.trim(), content: '', enabled: true, sort }))
}

async function remove(k: Knowledge) {
  if (!(await ElMessageBox.confirm(`刪除「${k.title}」？`, '刪除段落', { type: 'warning', confirmButtonText: '刪除', cancelButtonText: '取消' }).catch(() => false))) return
  await automationApi.deleteKnowledge(props.accountId, k.id)
  items.value = items.value.filter((x) => x.id !== k.id)
}
</script>

<template>
  <div>
    <div class="bar">
      <span class="count">{{ items.length }} 個段落</span>
      <DistillHint :pending="distillPending ?? 0" @go="emit('go-distill')" />
    </div>
    <div v-loading="loading" class="list">
      <div v-for="k in items" :key="k.id" class="item lc-card" :class="{ off: !k.enabled }">
        <div class="i-head">
          <el-input v-model="k.title" class="title" @input="markDirty(k)" />
          <el-tooltip :content="k.enabled ? '給 AI 參考中' : '已停用'" placement="top">
            <el-switch v-model="k.enabled" size="small" @change="save(k)" />
          </el-tooltip>
          <el-button text circle :icon="Delete" @click="remove(k)" />
        </div>
        <el-input
          v-model="k.content"
          type="textarea"
          :autosize="{ minRows: 3, maxRows: 14 }"
          :placeholder="hints[k.title] ?? '寫下 AI 需要知道的內容'"
          @input="markDirty(k)"
        />
        <div class="i-foot">
          <span v-if="!k.content.trim()" class="warn">尚未填寫</span>
          <span v-else class="ok">{{ k.content.trim().length }} 字</span>
          <el-button
            size="small"
            type="primary"
            :disabled="!dirty.has(k.id)"
            :loading="saving === k.id"
            @click="save(k)"
          >
            儲存
          </el-button>
        </div>
      </div>
    </div>

    <el-button class="add" :icon="Plus" @click="add">新增段落</el-button>

    <section class="ig lc-card">
      <div class="ig-head">
        <h3>
          IG 貼文（自動同步）
          <HelpTip text="AI 只讀貼文文案，不看圖片；過期的活動、優惠只會當成「某天曾公告」，不會當成現在有效。這裡列最近 30 篇已發佈貼文，關掉單篇＝不給 AI 參考。" />
        </h3>
        <el-switch :model-value="postsKb" :loading="postsKbSaving" aria-label="AI 參考近期 IG 貼文" @change="(v) => setPostsKb(v as boolean)" />
      </div>
      <div v-loading="igLoading" class="ig-list" :class="{ off: !postsKb }">
        <div v-for="p in igPosts" :key="p.id" class="ig-item">
          <div class="thumb">
            <img v-if="postThumb(p)" :src="postThumb(p)" alt="" />
            <span v-else>{{ kindLabels[p.kind] }}</span>
          </div>
          <div class="ig-main">
            <div class="ig-meta">{{ shortDate(p.publishedAt ?? p.scheduledAt) }} · {{ kindLabels[p.kind] }}<em v-if="p.source === 'ig'" class="ig-tag">IG 發佈</em></div>
            <p class="ig-cap" :class="{ none: !p.caption }">{{ p.caption || '（無文案，AI 沒東西可參考）' }}</p>
          </div>
          <el-switch
            :model-value="!p.aiExclude"
            :loading="togglingPost === p.id"
            :disabled="!postsKb"
            aria-label="給 AI 參考"
            @change="(v) => setPostRef(p, v as boolean)"
          />
        </div>
        <div v-if="!igLoading && !igPosts.length" class="ig-empty">還沒有已發佈的貼文；到「排程」按「同步 IG 貼文」就會出現在這裡</div>
      </div>
    </section>
  </div>
</template>

<style scoped lang="scss">
.bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  margin-bottom: 14px;
}
.count {
  font-size: 13px;
  color: var(--lc-muted);
}
.list {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(380px, 1fr));
  gap: 14px;
  min-height: 80px;
}
.item {
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  &.off {
    opacity: 0.6;
  }
}
.i-head {
  display: flex;
  align-items: center;
  gap: 8px;
  .title :deep(.el-input__wrapper) {
    box-shadow: none;
    padding-left: 2px;
    font-weight: 700;
    font-size: 15px;
  }
}
.i-foot {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 12px;
  .warn {
    color: var(--lc-tone-amber-fg);
  }
  .ok {
    color: var(--lc-muted);
  }
}
.add {
  margin-top: 14px;
}
.ig {
  margin-top: 24px;
  padding: 14px;
}
.ig-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  h3 {
    margin: 0;
    font-size: 15px;
  }
}
.ig-list {
  margin-top: 10px;
  min-height: 60px;
  &.off {
    opacity: 0.55;
  }
}
.ig-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 0;
  border-top: 1px solid var(--lc-border);
  &:first-child {
    border-top: 0;
  }
}
.thumb {
  flex: none;
  width: 52px;
  height: 52px;
  border-radius: 8px;
  overflow: hidden;
  display: grid;
  place-items: center;
  background: var(--lc-bg-thumb);
  font-size: 11px;
  color: var(--lc-muted);
  img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
}
.ig-main {
  flex: 1;
  min-width: 0;
}
.ig-meta {
  font-size: 12px;
  color: var(--lc-muted);
}
.ig-tag {
  margin-left: 6px;
  padding: 0 6px;
  border-radius: 999px;
  font-style: normal;
  font-size: 11px;
  background: var(--lc-tone-caramel-bg);
  color: var(--lc-tone-caramel-fg);
}
.ig-cap {
  margin: 2px 0 0;
  font-size: 14px;
  line-height: 1.5;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  white-space: pre-line;
  word-break: break-word;
  &.none {
    color: var(--lc-muted);
  }
}
.ig-empty {
  padding: 14px 0 4px;
  text-align: center;
  font-size: 13px;
  color: var(--lc-muted);
}
@media (max-width: 768px) {
  .list {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 768px) {
  // 手機字級 ≥13px（桌機維持原值）
  .i-foot {
    font-size: 13px;
  }
  .ig-meta,
  .ig-empty {
    font-size: 13px;
  }
}
</style>
