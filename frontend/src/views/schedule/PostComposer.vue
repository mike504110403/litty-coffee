<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Close, ArrowLeft, ArrowRight, MagicStick, Link } from '@element-plus/icons-vue'
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
import PostInsights from './PostInsights.vue'
import { useIsMobile } from '@/composables/useIsMobile'

const props = defineProps<{ post: Post | null; defaultDate: Date | null; bestHours: number[] }>()
const visible = defineModel<boolean>({ required: true })
const emit = defineEmits<{ saved: []; insightsUpdated: [post: Post] }>()

const isMobile = useIsMobile()

const form = reactive({
  id: 0,
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
  form.kind = p?.kind ?? 'image'
  form.caption = p?.caption ?? ''
  form.media = [...(p?.media ?? [])]
  form.when = p?.scheduledAt ? new Date(p.scheduledAt) : props.defaultDate
  mobileTab.value = 'edit'
  aiBrief.value = ''
})

// ---- 媒體 ----
const accept = computed(() =>
  form.kind === 'image' ? 'image/jpeg,image/png' : form.kind === 'reel' ? 'video/mp4,video/quicktime' : 'image/jpeg,image/png,video/mp4,video/quicktime',
)
const maxMedia = computed(() => (form.kind === 'carousel' ? 10 : 1))
const uploading = ref<{ name: string; pct: number }[]>([])
const fileInput = ref<HTMLInputElement>()

async function onFiles(e: Event) {
  const files = Array.from((e.target as HTMLInputElement).files ?? [])
  ;(e.target as HTMLInputElement).value = ''
  const room = maxMedia.value - form.media.length
  if (files.length > room) ElMessage.warning(`${kindLabels[form.kind]}最多 ${maxMedia.value} 個，多的已略過`)
  for (const f of files.slice(0, Math.max(0, room))) {
    const item = reactive({ name: f.name, pct: 0 })
    uploading.value.push(item)
    try {
      const a = await postsApi.upload(f, (p) => (item.pct = p))
      if (maxMedia.value === 1) form.media = [a]
      else form.media.push(a)
    } catch {
      // http 攔截器已顯示錯誤
    } finally {
      uploading.value = uploading.value.filter((x) => x !== item)
    }
  }
}

function move(i: number, d: -1 | 1) {
  const j = i + d
  if (j < 0 || j >= form.media.length) return
  const arr = form.media
  ;[arr[i], arr[j]] = [arr[j], arr[i]]
}

watch(
  () => form.kind,
  (k) => {
    if (k !== 'carousel' && form.media.length > 1) form.media = form.media.slice(0, 1)
  },
)

const ratioWarning = computed(() => {
  if (form.kind !== 'image') return ''
  const a = form.media[0]
  if (!a || a.kind !== 'image' || !a.height) return ''
  const r = a.width / a.height
  return r < 0.795 || r > 1.915 ? `圖片比例 ${r.toFixed(2)} 不在 IG 允許範圍（4:5～1.91:1），請先裁切` : ''
})

// ---- 文案 ----
const captionLen = computed(() => [...form.caption].length)
const tags = computed(() => countHashtags(form.caption))

const aiBrief = ref('')
const aiBusy = ref(false)
const aiOpen = ref(false)
async function runAI() {
  if (!aiBrief.value.trim()) return
  if (form.caption.trim()) {
    const ok = await ElMessageBox.confirm('要用 AI 的版本取代目前的文案嗎？', 'AI 幫寫', {
      confirmButtonText: '取代',
      cancelButtonText: '取消',
    }).catch(() => false)
    if (!ok) return
  }
  aiBusy.value = true
  try {
    form.caption = (await postsApi.caption(aiBrief.value, form.kind)).caption
    aiOpen.value = false
  } finally {
    aiBusy.value = false
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
const saving = ref<SaveAction | null>(null)
async function submit(action: SaveAction) {
  saving.value = action
  try {
    await postsApi.save(
      {
        id: form.id || undefined,
        kind: form.kind,
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
          <label>類型</label>
          <el-segmented
            v-model="form.kind"
            :disabled="readonly"
            :options="(Object.keys(kindLabels) as PostKind[]).map((k) => ({ label: kindLabels[k], value: k }))"
          />
        </div>

        <div class="field">
          <label>
            {{ form.kind === 'carousel' ? `圖片／影片（${form.media.length}/10，可調順序）` : form.kind === 'reel' ? '影片' : '圖片' + (form.kind === 'story' ? '或影片' : '') }}
          </label>
          <div class="media-grid">
            <div v-for="(m, i) in form.media" :key="m.id" class="thumb">
              <video v-if="m.kind === 'video'" :src="m.url" muted />
              <img v-else :src="m.url" alt="" />
              <span v-if="form.kind === 'carousel'" class="num">{{ i + 1 }}</span>
              <div v-if="!readonly" class="ops">
                <button v-if="form.kind === 'carousel' && i > 0" aria-label="往前" @click="move(i, -1)">
                  <el-icon><ArrowLeft /></el-icon>
                </button>
                <button v-if="form.kind === 'carousel' && i < form.media.length - 1" aria-label="往後" @click="move(i, 1)">
                  <el-icon><ArrowRight /></el-icon>
                </button>
                <button aria-label="移除" @click="form.media.splice(i, 1)"><el-icon><Close /></el-icon></button>
              </div>
            </div>
            <div v-for="u in uploading" :key="u.name" class="thumb uploading">
              <el-progress type="circle" :percentage="u.pct" :width="56" />
            </div>
            <button
              v-if="!readonly && form.media.length + uploading.length < maxMedia"
              class="thumb add"
              @click="fileInput?.click()"
            >
              <el-icon :size="22"><Plus /></el-icon>
              <span>上傳</span>
            </button>
          </div>
          <input ref="fileInput" type="file" :accept="accept" :multiple="form.kind === 'carousel'" hidden @change="onFiles" />
          <p v-if="ratioWarning" class="warn">{{ ratioWarning }}</p>
          <p v-if="!readonly" class="hint">圖片 JPEG／PNG（PNG 會自動轉 JPEG）、最大 8MB；影片 MP4／MOV。</p>
        </div>

        <div class="field">
          <div class="label-row">
            <label>文案</label>
            <el-popover v-if="!readonly && form.kind !== 'story'" v-model:visible="aiOpen" trigger="click" :width="300" placement="bottom-end">
              <template #reference>
                <el-button size="small" round :icon="MagicStick">AI 幫寫</el-button>
              </template>
              <div class="ai-pop">
                <p>這篇想說什麼？AI 會依店家知識庫寫文案。</p>
                <el-input v-model="aiBrief" type="textarea" :rows="3" placeholder="例：秋季新品桂花燕麥拿鐵，週末買一送一" />
                <el-button type="primary" size="small" :loading="aiBusy" :disabled="!aiBrief.trim()" @click="runAI">產生文案</el-button>
              </div>
            </el-popover>
          </div>
          <el-input
            v-if="form.kind !== 'story'"
            v-model="form.caption"
            type="textarea"
            :autosize="{ minRows: 5, maxRows: 12 }"
            :disabled="readonly"
            placeholder="寫點什麼…"
          />
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
        <IgPreview :kind="form.kind" :media="form.media" :caption="form.caption" />
      </div>
    </div>

    <template #footer>
      <div class="footer">
        <el-button v-if="post && post.status !== 'publishing'" text type="danger" @click="remove">刪除</el-button>
        <span class="spacer" />
        <template v-if="!readonly">
          <el-button :loading="saving === 'draft'" @click="submit('draft')">存草稿</el-button>
          <el-button :loading="saving === 'now'" :disabled="!form.media.length" @click="submit('now')">立即發佈</el-button>
          <el-button type="primary" :loading="saving === 'schedule'" :disabled="!form.media.length || !form.when" @click="submit('schedule')">
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
.media-grid {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.thumb {
  position: relative;
  width: 96px;
  height: 96px;
  border-radius: 12px;
  overflow: hidden;
  background: var(--lc-bg-thumb);
  img,
  video {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .num {
    position: absolute;
    top: 6px;
    left: 6px;
    min-width: 20px;
    height: 20px;
    border-radius: 10px;
    background: var(--lc-scrim-badge);
    color: var(--lc-on-fill);
    font-size: 11px;
    display: grid;
    place-items: center;
  }
  .ops {
    position: absolute;
    inset: auto 0 0 0;
    display: flex;
    justify-content: flex-end;
    gap: 2px;
    padding: 4px;
    background: var(--lc-scrim-grad);
    button {
      width: 24px;
      height: 24px;
      border: 0;
      border-radius: 6px;
      background: var(--lc-scrim-btn);
      display: grid;
      place-items: center;
      cursor: pointer;
    }
  }
  &.uploading {
    display: grid;
    place-items: center;
  }
  &.add {
    border: 1.5px dashed var(--el-color-primary-light-5);
    background: var(--el-color-primary-light-9);
    color: var(--el-color-primary);
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 4px;
    font: inherit;
    font-size: 12px;
    cursor: pointer;
  }
}
.warn {
  color: var(--lc-fail-fg);
  font-size: 12px;
  margin: 8px 0 0;
}
.hint {
  color: var(--lc-muted);
  font-size: 12px;
  margin: 6px 0 0;
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
</style>
