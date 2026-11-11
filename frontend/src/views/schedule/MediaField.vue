<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Plus, Close, RefreshRight, WarningFilled } from '@element-plus/icons-vue'
import Sortable from 'sortablejs'
import { kindLabels, postsApi, type MediaAsset, type PostKind } from '@/api/posts'
import HelpTip from '@/components/HelpTip.vue'

const props = defineProps<{ kind: PostKind; account: number | null; readonly: boolean }>()
// 貼文（kind=image）收 1～10 個圖片／影片，存檔時依張數轉 image／carousel（PostComposer）
const kind = computed(() => props.kind)
const media = defineModel<MediaAsset[]>('media', { required: true })

// 上傳中／失敗的項目；seq 決定完成後插入的位置，讓順序跟選檔順序一致
interface Pending {
  key: number
  seq: number
  file: File
  pct: number
  failed: boolean
  preview: string
}
const MAX_POST = 10
const pending = ref<Pending[]>([])
const seqOf = new Map<number, number>() // 本次編輯上傳的媒體 id → seq
let counter = 0

const accepts = computed(() => {
  if (kind.value === 'reel') return ['video/mp4', 'video/quicktime']
  return ['image/jpeg', 'image/png', 'video/mp4', 'video/quicktime']
})
const accept = computed(() => accepts.value.join(','))
const isPost = computed(() => kind.value === 'image')
const multiple = computed(() => isPost.value)
const count = computed(() => media.value.length + pending.value.length)
const showAdd = computed(() => !props.readonly && count.value < (isPost.value ? MAX_POST : 1))

const fileInput = ref<HTMLInputElement>()
const grid = ref<HTMLElement>()
const dragOver = ref(false)
const announce = ref('')

function onPick(e: Event) {
  const input = e.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  input.value = ''
  addFiles(files)
}

function addFiles(list: File[]) {
  if (props.readonly || !list.length) return
  if (props.account == null) {
    ElMessage.warning('請先選擇要發佈的 IG 帳號')
    return
  }
  let files = list.filter((f) => accepts.value.includes(f.type))
  if (files.length < list.length) ElMessage.warning(`已略過 ${list.length - files.length} 個不支援的檔案`)
  if (!files.length) return

  if (isPost.value) {
    const room = MAX_POST - count.value
    if (files.length > room) {
      ElMessage.warning(room > 0 ? `貼文最多 ${MAX_POST} 個，已略過 ${files.length - room} 個` : `貼文最多 ${MAX_POST} 個`)
      files = files.slice(0, Math.max(0, room))
    }
  } else {
    if (count.value >= 1) {
      ElMessage.warning(`${kindLabels[kind.value]}只能放 1 個，請先移除目前的`)
      return
    }
    if (files.length > 1) {
      ElMessage.warning(`${kindLabels[kind.value]}只能放 1 個，已保留第一個`)
      files = files.slice(0, 1)
    }
  }
  for (const file of files) {
    const p = reactive<Pending>({
      key: ++counter,
      seq: counter,
      file,
      pct: 0,
      failed: false,
      preview: file.type.startsWith('image/') ? URL.createObjectURL(file) : '',
    })
    pending.value.push(p)
    void run(p)
  }
}

async function run(p: Pending) {
  const acc = props.account
  if (acc == null) return
  p.failed = false
  p.pct = 0
  try {
    const a = await postsApi.upload(acc, p.file, (v) => (p.pct = v))
    if (props.account !== acc || !pending.value.includes(p)) return // 上傳途中換帳號或已取消
    seqOf.set(a.id, p.seq)
    let at = media.value.length
    for (let i = 0; i < media.value.length; i++) {
      const s = seqOf.get(media.value[i].id)
      if (s !== undefined && s > p.seq) {
        at = i
        break
      }
    }
    const next = [...media.value]
    if (isPost.value) next.splice(at, 0, a)
    else next.splice(0, next.length, a)
    media.value = next
    dropPending(p)
  } catch {
    // http 攔截器已顯示錯誤
    if (pending.value.includes(p)) p.failed = true
  }
}

function dropPending(p: Pending) {
  if (p.preview) URL.revokeObjectURL(p.preview)
  pending.value = pending.value.filter((x) => x !== p)
}

const removeAt = (i: number) => (media.value = media.value.filter((_, j) => j !== i))

function reorder(from: number, to: number) {
  if (from === to || to < 0 || to >= media.value.length) return
  const next = [...media.value]
  next.splice(to, 0, next.splice(from, 1)[0])
  media.value = next
}

function onKey(e: KeyboardEvent, i: number) {
  if (props.readonly || e.target !== e.currentTarget) return
  const d = e.key === 'ArrowLeft' ? -1 : e.key === 'ArrowRight' ? 1 : 0
  if (!d) return
  e.preventDefault()
  const j = i + d
  if (j < 0 || j >= media.value.length) return
  reorder(i, j)
  announce.value = `已移到第 ${j + 1} 張，共 ${media.value.length} 張`
  void nextTick(() => grid.value?.querySelectorAll<HTMLElement>('.item')[j]?.focus())
}

// ---- 拖曳排序（SortableJS）：桌機滑鼠、手機長按後觸控拖曳 ----
let sortable: Sortable | null = null
onMounted(() => {
  if (!grid.value) return
  sortable = Sortable.create(grid.value, {
    animation: 150,
    draggable: '.item',
    filter: '.del',
    preventOnFilter: false,
    ghostClass: 'is-ghost',
    chosenClass: 'is-chosen',
    delay: 150, // 觸控長按才開始拖，保留捲動
    delayOnTouchOnly: true,
    touchStartThreshold: 6,
    disabled: props.readonly,
    onEnd(evt) {
      const { item, from, oldIndex, oldDraggableIndex, newDraggableIndex } = evt
      // 把 DOM 還原，交給 Vue 依陣列順序重繪，避免 DOM 與 vdom 分歧
      if (oldIndex !== undefined) {
        from.removeChild(item)
        from.insertBefore(item, from.children[oldIndex] ?? null)
      }
      if (oldDraggableIndex === undefined || newDraggableIndex === undefined) return
      reorder(oldDraggableIndex, newDraggableIndex)
    },
  })
})
watch(
  () => props.readonly,
  (r) => sortable?.option('disabled', r),
)

// ---- 拖檔案進上傳區 ----
const hasFiles = (e: DragEvent) => !!e.dataTransfer && Array.from(e.dataTransfer.types).includes('Files')
function onDragOver(e: DragEvent) {
  if (props.readonly || !hasFiles(e)) return
  e.preventDefault()
  dragOver.value = true
}
function onDragLeave(e: DragEvent) {
  if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) dragOver.value = false
}
function onDrop(e: DragEvent) {
  if (!hasFiles(e)) return
  e.preventDefault()
  dragOver.value = false
  addFiles(Array.from(e.dataTransfer?.files ?? []))
}

onBeforeUnmount(() => {
  sortable?.destroy()
  pending.value.forEach((p) => p.preview && URL.revokeObjectURL(p.preview))
})

const label = computed(() =>
  isPost.value ? `圖片／影片（${media.value.length}/${MAX_POST}）` : kind.value === 'reel' ? '影片' : '圖片或影片',
)

// 單張貼文的圖片比例；單支影片貼文無法發佈（後端規則：單張貼文＝1 張圖片）
const ratioWarning = computed(() => {
  if (kind.value !== 'image' || media.value.length !== 1) return ''
  const a = media.value[0]
  if (a?.kind === 'video') return '單支影片請改用 Reels；貼文需 1 張圖片，或 2～10 個圖片／影片'
  if (!a || a.kind !== 'image' || !a.height) return ''
  const r = a.width / a.height
  return r < 0.795 || r > 1.915 ? `圖片比例 ${r.toFixed(2)} 不在 IG 允許範圍（4:5～1.91:1），請先裁切` : ''
})
</script>

<template>
  <div class="field">
    <label>
      {{ label }}
      <HelpTip v-if="!readonly" placement="right">
        圖片 JPEG／PNG（PNG 會自動轉 JPEG）、最大 8MB；影片 MP4／MOV。<br />
        可一次選多張或把檔案拖進來；貼文最多 10 個，多個會以輪播發佈。<br />
        拖曳縮圖可調順序（手機長按後拖），或聚焦後按左右方向鍵。
      </HelpTip>
    </label>
    <div
      ref="grid"
      class="media-grid"
      :class="{ 'drag-over': dragOver }"
      role="list"
      @dragover="onDragOver"
      @dragleave="onDragLeave"
      @drop="onDrop"
    >
      <div
        v-for="(m, i) in media"
        :key="m.id"
        class="thumb item"
        :class="{ sortable: !readonly && media.length > 1 }"
        role="listitem"
        :tabindex="readonly ? undefined : 0"
        :aria-label="media.length > 1 ? `第 ${i + 1} 張，共 ${media.length} 張，可按左右方向鍵調整順序` : '已上傳的媒體'"
        @keydown="onKey($event, i)"
      >
        <video v-if="m.kind === 'video'" :src="m.url" muted draggable="false" />
        <img v-else :src="m.url" alt="" draggable="false" />
        <span v-if="media.length > 1" class="num">{{ i + 1 }}</span>
        <button v-if="!readonly" type="button" class="del" :aria-label="`移除第 ${i + 1} 個媒體`" @click.stop="removeAt(i)">
          <el-icon :size="12"><Close /></el-icon>
        </button>
      </div>
      <div v-for="u in pending" :key="u.key" class="thumb uploading" :class="{ failed: u.failed }" role="listitem">
        <img v-if="u.preview" :src="u.preview" alt="" draggable="false" />
        <div class="cover">
          <template v-if="u.failed">
            <el-icon :size="16"><WarningFilled /></el-icon>
            <button type="button" class="retry" :aria-label="`重試上傳 ${u.file.name}`" @click="run(u)">
              <el-icon :size="18"><RefreshRight /></el-icon>
              <span>重試</span>
            </button>
          </template>
          <el-progress v-else type="circle" :percentage="u.pct" :width="48" :stroke-width="4" />
        </div>
        <button type="button" class="del" :aria-label="`取消 ${u.file.name}`" @click.stop="dropPending(u)">
          <el-icon :size="12"><Close /></el-icon>
        </button>
      </div>
      <button v-if="showAdd" type="button" class="thumb add" @click="fileInput?.click()">
        <el-icon :size="22"><Plus /></el-icon>
        <span>上傳／拖入</span>
      </button>
    </div>
    <input ref="fileInput" type="file" :accept="accept" :multiple="multiple" hidden @change="onPick" />
    <span class="sr-only" aria-live="polite">{{ announce }}</span>
    <p v-if="ratioWarning" class="warn">{{ ratioWarning }}</p>
  </div>
</template>

<style scoped lang="scss">
.field {
  margin-bottom: 18px;
  label {
    display: block;
    font-size: 13px;
    font-weight: 500;
    margin-bottom: 8px;
  }
}
.media-grid {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  padding: 4px;
  margin: -4px;
  border-radius: 14px;
  border: 1.5px dashed transparent;
  transition: background 0.15s, border-color 0.15s;
  &.drag-over {
    border-color: var(--el-color-primary);
    background: var(--el-color-primary-light-9);
  }
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
    display: block;
    -webkit-user-drag: none;
    user-select: none;
  }
  .num {
    position: absolute;
    top: 6px;
    left: 6px;
    min-width: 20px;
    height: 20px;
    padding: 0 4px;
    box-sizing: border-box;
    border-radius: 10px;
    background: var(--lc-scrim-badge);
    color: var(--lc-on-fill);
    font-size: 11px;
    display: grid;
    place-items: center;
    pointer-events: none;
  }
  .del {
    position: absolute;
    top: 5px;
    right: 5px;
    width: 22px;
    height: 22px;
    padding: 0;
    border: 0;
    border-radius: 50%;
    background: var(--lc-scrim-badge);
    color: var(--lc-on-fill);
    display: grid;
    place-items: center;
    cursor: pointer;
    z-index: 2;
    // 觸控裝置常駐；桌機 hover／聚焦才顯示
    &:hover {
      background: rgba(0, 0, 0, 0.8);
    }
    &:focus-visible {
      outline: 2px solid var(--lc-on-fill);
      outline-offset: 1px;
    }
  }
  &.item {
    cursor: default;
    &.sortable {
      cursor: grab;
    }
    &:focus-visible {
      outline: 2px solid var(--el-color-primary);
      outline-offset: 2px;
    }
  }
  &.is-ghost {
    opacity: 0.45;
    outline: 2px dashed var(--el-color-primary);
    outline-offset: -2px;
    img,
    video {
      filter: grayscale(0.6);
    }
  }
  &.is-chosen {
    cursor: grabbing;
  }
  &.uploading {
    .cover {
      position: absolute;
      inset: 0;
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      gap: 4px;
      background: rgba(255, 255, 255, 0.6);
      color: var(--lc-fail-fg);
    }
    &.failed .cover {
      background: rgba(255, 255, 255, 0.8);
    }
    .retry {
      display: flex;
      align-items: center;
      gap: 2px;
      border: 0;
      border-radius: 12px;
      padding: 3px 8px;
      background: var(--lc-fail-fg);
      color: var(--lc-on-fill);
      font: inherit;
      font-size: 12px;
      cursor: pointer;
    }
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
:global(html.dark) .thumb.uploading .cover {
  background: rgba(0, 0, 0, 0.55);
}
:global(html.dark) .thumb.uploading.failed .cover {
  background: rgba(0, 0, 0, 0.7);
}
@media (hover: hover) {
  .thumb.item .del {
    opacity: 0;
    transition: opacity 0.12s;
  }
  .thumb.item:hover .del,
  .thumb.item:focus-within .del {
    opacity: 1;
  }
}
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0 0 0 0);
  white-space: nowrap;
}
.warn {
  color: var(--lc-fail-fg);
  font-size: 12px;
  margin: 8px 0 0;
}
</style>
