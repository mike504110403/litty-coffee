<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Loading, MagicStick, Link } from '@element-plus/icons-vue'
import {
  countHashtags,
  kindLabels,
  MAX_CAPTION,
  MAX_HASHTAGS,
  postsApi,
  statusLabels,
  type MediaAsset,
  type Post,
  type PostKind,
  type SaveAction,
} from '@/api/posts'
import IgPreview from '@/components/IgPreview.vue'
import AccountSelect from '@/components/AccountSelect.vue'
import { useAccountsStore } from '@/stores/accounts'
import PostInsights from './PostInsights.vue'
import MediaField from './MediaField.vue'
import { useIsMobile } from '@/composables/useIsMobile'

// defaultAccount：新貼文預設的帳號（例如行事曆正篩選某帳號）；沒給就用上次用的
const props = defineProps<{ post: Post | null; defaultDate: Date | null; bestHours: number[]; defaultAccount?: number | null }>()
const visible = defineModel<boolean>({ required: true })
const emit = defineEmits<{ saved: []; insightsUpdated: [post: Post] }>()

const isMobile = useIsMobile()

const accounts = useAccountsStore()
const form = reactive({
  id: 0,
  account: null as number | null,
  kind: 'image' as PostKind,
  caption: '',
  media: [] as MediaAsset[],
  when: null as Date | null,
})
const readonly = computed(() => !!props.post && ['publishing', 'published'].includes(props.post.status))
const mobileTab = ref<'edit' | 'preview'>('edit')

watch(visible, (v) => {
  if (!v) return
  const p = props.post
  form.id = p?.id ?? 0
  form.account = p?.igAccountId ?? props.defaultAccount ?? accounts.lastUsed()
  form.kind = !p || p.kind === 'carousel' ? 'image' : p.kind // 輪播併入「貼文」
  form.caption = p?.caption ?? ''
  form.media = [...(p?.media ?? [])]
  form.when = p?.scheduledAt ? new Date(p.scheduledAt) : props.defaultDate
  mobileTab.value = 'edit'
  aiBrief.value = ''
  aiGen++
  aiBusy.value = false
  stopAiTimer()
})

// ---- 帳號 ----
const account = computed(() => (form.account == null ? undefined : accounts.byId.get(form.account)))
// 媒體只能用在上傳時選的帳號：換帳號時清掉別的帳號的媒體
watch(
  () => form.account,
  (id) => {
    if (id == null) return
    const before = form.media.length
    form.media = form.media.filter((m) => m.igAccountId === id)
    if (form.media.length < before) ElMessage.info('已移除屬於其他帳號的媒體，請重新上傳')
  },
)

// ---- 媒體（上傳、排序、刪除在 MediaField）----
watch(
  () => form.kind,
  (k) => {
    if ((k === 'reel' || k === 'story') && form.media.length > 1) form.media = form.media.slice(0, 1)
  },
)

// ---- 文案 ----
const captionLen = computed(() => [...form.caption].length)
const tags = computed(() => countHashtags(form.caption))

const aiBrief = ref('')
const aiBusy = ref(false)
const aiOpen = ref(false)
const aiSecs = ref(0)
let aiTimer: ReturnType<typeof setInterval> | undefined
let aiGen = 0 // 對話框重開後，舊請求的結果作廢
const stopAiTimer = () => {
  clearInterval(aiTimer)
  aiTimer = undefined
}
onBeforeUnmount(stopAiTimer)
async function runAI() {
  if (form.account == null || aiBusy.value) return
  const imageIds = form.media.filter((m) => m.kind === 'image').map((m) => m.id) // AI 會看圖（後端最多取 4 張）
  if (!aiBrief.value.trim() && !imageIds.length) {
    ElMessage.warning('請輸入想說的重點，或先上傳圖片')
    return
  }
  if (form.caption.trim()) {
    const ok = await ElMessageBox.confirm('要用 AI 的版本取代目前的文案嗎？', 'AI 幫寫', {
      confirmButtonText: '取代',
      cancelButtonText: '取消',
    }).catch(() => false)
    if (!ok) return
  }
  const gen = ++aiGen
  aiBusy.value = true
  aiOpen.value = false
  aiSecs.value = 0
  aiTimer = setInterval(() => aiSecs.value++, 1000)
  try {
    const r = await postsApi.caption(form.account, aiBrief.value, derivedKind.value, imageIds)
    if (gen === aiGen) form.caption = r.caption
  } catch {
    // http 攔截器已顯示錯誤；文案維持原樣
  } finally {
    if (gen === aiGen) {
      aiBusy.value = false
      stopAiTimer()
    }
  }
}

// ---- 時間 ----
function setHour(h: number) {
  const base = form.when ? new Date(form.when) : new Date()
  base.setHours(h, 0, 0, 0)
  if (base.getTime() < Date.now()) base.setDate(base.getDate() + 1)
  form.when = base
}
const disabledDate = (d: Date) => d.getTime() < new Date().setHours(0, 0, 0, 0)

// ---- 送出 ----
// 「貼文」依張數決定：1 張 image、2～10 張 carousel
const derivedKind = computed<PostKind>(() => (form.kind === 'image' && form.media.length > 1 ? 'carousel' : form.kind))
const saving = ref<SaveAction | null>(null)
async function submit(action: SaveAction) {
  if (form.account == null) {
    ElMessage.warning('請選擇要發佈的 IG 帳號')
    return
  }
  accounts.remember(form.account)
  saving.value = action
  try {
    await postsApi.save(
      {
        id: form.id || undefined,
        igAccountId: form.account,
        kind: derivedKind.value,
        caption: form.kind === 'story' ? '' : form.caption,
        mediaIds: form.media.map((m) => m.id),
        scheduledAt: form.when ? form.when.toISOString() : null,
      },
      action,
    )
    ElMessage.success(action === 'draft' ? '已存草稿' : action === 'now' ? '已送出發佈' : '已排程')
    visible.value = false
    emit('saved')
  } finally {
    saving.value = null
  }
}

async function remove() {
  if (!props.post) return
  const published = props.post.status === 'published'
  const ok = await ElMessageBox.confirm(
    published ? '只會刪除後台紀錄，IG 上的貼文不會被刪除。' : '確定刪除這篇貼文？',
    '刪除',
    { type: 'warning', confirmButtonText: '刪除', cancelButtonText: '取消' },
  ).catch(() => false)
  if (!ok) return
  await postsApi.remove(props.post.id)
  visible.value = false
  emit('saved')
}

const title = computed(() => {
  if (!props.post) return '新增貼文'
  return `${kindLabels[props.post.kind]} · ${statusLabels[props.post.status]}`
})
</script>

<template>
  <el-dialog v-model="visible" :title="title" :fullscreen="isMobile" width="960px" top="5vh" destroy-on-close class="composer">
    <el-segmented
      v-if="isMobile"
      v-model="mobileTab"
      :options="[
        { label: '編輯', value: 'edit' },
        { label: '預覽', value: 'preview' },
      ]"
      block
      class="m-tabs"
    />
    <div class="layout">
      <div v-show="!isMobile || mobileTab === 'edit'" class="edit">
        <el-alert
          v-if="post?.status === 'failed'"
          type="error"
          :closable="false"
          show-icon
          :title="`發佈失敗（已嘗試 ${post.attempts} 次）`"
          :description="post.error"
          class="mb"
        />
        <el-alert
          v-else-if="post?.status === 'scheduled' && post.error"
          type="warning"
          :closable="false"
          show-icon
          title="上次嘗試失敗，系統會自動重試"
          :description="post.error"
          class="mb"
        />
        <div v-if="post?.status === 'published'" class="published mb">
          已於 {{ new Date(post.publishedAt!).toLocaleString('zh-TW') }} 發佈
          <el-link v-if="post.permalink" :href="post.permalink" target="_blank" type="primary" :icon="Link">在 IG 查看</el-link>
        </div>
        <section v-if="post?.status === 'published'" class="insights-box mb">
          <h4>貼文成效</h4>
          <PostInsights :post="post" @updated="(p) => emit('insightsUpdated', p)" />
        </section>

        <div class="field">
          <label>發佈到</label>
          <AccountSelect v-model="form.account" :disabled="readonly" class="acc-select" />
        </div>

        <div class="field">
          <label>類型</label>
          <el-segmented
            v-model="form.kind"
            :disabled="readonly"
            :options="(['image', 'reel', 'story'] as PostKind[]).map((k) => ({ label: kindLabels[k], value: k }))"
          />
        </div>

        <MediaField v-model:kind="form.kind" v-model:media="form.media" :account="form.account" :readonly="readonly" />

        <div class="field">
          <div class="label-row">
            <label>文案</label>
            <el-popover v-if="!readonly && form.kind !== 'story'" v-model:visible="aiOpen" trigger="click" :width="300" placement="bottom-end">
              <template #reference>
                <el-button size="small" round :icon="MagicStick" :loading="aiBusy" :disabled="aiBusy">AI 幫寫</el-button>
              </template>
              <div class="ai-pop">
                <p>想說什麼？有圖片時可留空，AI 會看圖寫。</p>
                <el-input v-model="aiBrief" type="textarea" :rows="3" placeholder="例：秋季新品桂花燕麥拿鐵，週末買一送一" />
                <el-button type="primary" size="small" :loading="aiBusy" :disabled="aiBusy" @click="runAI">產生文案</el-button>
              </div>
            </el-popover>
          </div>
          <div v-if="form.kind !== 'story'" class="caption-box">
            <el-input
              v-model="form.caption"
              type="textarea"
              :autosize="{ minRows: 5, maxRows: 12 }"
              :disabled="readonly || aiBusy"
              placeholder="寫點什麼…"
            />
            <div v-if="aiBusy" class="ai-mask" role="status" aria-live="polite">
              <el-icon class="spin" :size="22"><Loading /></el-icon>
              <span>AI 撰寫中…{{ aiSecs }} 秒</span>
            </div>
          </div>
          <p v-else class="hint">IG 限動不支援文案。</p>
          <div v-if="form.kind !== 'story'" class="counter">
            <span :class="{ over: captionLen > MAX_CAPTION }">{{ captionLen }} / {{ MAX_CAPTION }}</span>
            <span :class="{ over: tags > MAX_HASHTAGS }"># {{ tags }} / {{ MAX_HASHTAGS }}</span>
          </div>
        </div>

        <div class="field">
          <label>發佈時間</label>
          <el-date-picker
            v-model="form.when"
            type="datetime"
            placeholder="選擇日期與時間"
            format="YYYY/MM/DD HH:mm"
            :disabled="readonly"
            :disabled-date="disabledDate"
            :clearable="true"
            class="picker"
          />
          <div v-if="bestHours.length && !readonly" class="best">
            <span>顧客最常傳訊的時段：</span>
            <el-tag v-for="h in bestHours" :key="h" effect="plain" round class="hour" @click="setHour(h)">{{ h }}:00</el-tag>
          </div>
        </div>
      </div>

      <div v-show="!isMobile || mobileTab === 'preview'" class="preview">
        <IgPreview
          :kind="form.kind"
          :media="form.media"
          :caption="form.caption"
          :username="account?.username"
          :avatar="account?.profilePic"
        />
      </div>
    </div>

    <template #footer>
      <div class="footer">
        <el-button v-if="post && post.status !== 'publishing'" text type="danger" @click="remove">刪除</el-button>
        <span class="spacer" />
        <template v-if="!readonly">
          <el-button :loading="saving === 'draft'" :disabled="aiBusy" @click="submit('draft')">存草稿</el-button>
          <el-button :loading="saving === 'now'" :disabled="!form.media.length || aiBusy" @click="submit('now')">立即發佈</el-button>
          <el-button type="primary" :loading="saving === 'schedule'" :disabled="!form.media.length || !form.when || aiBusy" @click="submit('schedule')">
            排程
          </el-button>
        </template>
        <el-button v-else @click="visible = false">關閉</el-button>
      </div>
    </template>
  </el-dialog>
</template>

<style scoped lang="scss">
:global(.el-dialog.composer.is-fullscreen) {
  display: flex;
  flex-direction: column;
}
:global(.el-dialog.composer.is-fullscreen .el-dialog__body) {
  flex: 1;
  overflow-y: auto;
}
:global(.el-dialog.composer.is-fullscreen .el-dialog__footer) {
  padding-bottom: calc(12px + env(safe-area-inset-bottom));
  border-top: 1px solid var(--lc-border);
}
.m-tabs {
  margin-bottom: 14px;
}
.layout {
  display: grid;
  grid-template-columns: 1fr 340px;
  gap: 24px;
}
.preview {
  position: sticky;
  top: 0;
  align-self: start;
  padding: 16px;
  border-radius: 16px;
  background: var(--lc-cream);
}
.mb {
  margin-bottom: 14px;
}
.published {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 14px;
  border-radius: 10px;
  background: var(--lc-ok-bg);
  color: var(--lc-ok-fg);
  font-size: 14px;
}
.insights-box {
  padding: 10px 14px 12px;
  border: 1px solid var(--lc-border);
  border-radius: 10px;
  h4 {
    margin: 0;
    font-size: 14px;
  }
}
.field {
  margin-bottom: 18px;
  label {
    display: block;
    font-size: 13px;
    font-weight: 500;
    margin-bottom: 8px;
  }
}
.label-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
  label {
    margin: 0;
  }
}
.caption-box {
  position: relative;
}
.ai-mask {
  position: absolute;
  inset: 0;
  z-index: 3;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  border-radius: var(--el-border-radius-base, 4px);
  background: color-mix(in srgb, var(--lc-surface) 72%, transparent);
  backdrop-filter: blur(1px);
  color: var(--el-color-primary);
  font-size: 13px;
  font-variant-numeric: tabular-nums;
  .spin {
    animation: ai-spin 1s linear infinite;
  }
}
@keyframes ai-spin {
  to {
    transform: rotate(360deg);
  }
}
@media (prefers-reduced-motion: reduce) {
  .ai-mask .spin {
    animation-duration: 3s;
  }
}
.counter {
  display: flex;
  justify-content: flex-end;
  gap: 14px;
  font-size: 12px;
  color: var(--lc-muted);
  margin-top: 4px;
  font-variant-numeric: tabular-nums;
  .over {
    color: var(--lc-fail-fg);
    font-weight: 700;
  }
}
.ai-pop {
  display: grid;
  gap: 8px;
  p {
    margin: 0;
    font-size: 13px;
    color: var(--lc-muted);
  }
  .el-button {
    justify-self: end;
  }
}
.picker {
  width: 100%;
  max-width: 280px;
}
.best {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-top: 8px;
  font-size: 12px;
  color: var(--lc-muted);
  .hour {
    cursor: pointer;
  }
}
.footer {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  .spacer {
    flex: 1;
  }
}
@media (max-width: 768px) {
  .layout {
    grid-template-columns: 1fr;
  }
  .preview {
    position: static;
  }
  .picker {
    max-width: none;
  }
  .footer .el-button + .el-button {
    margin-left: 0;
  }
}
.acc-select {
  width: 260px;
  max-width: 100%;
}
</style>
