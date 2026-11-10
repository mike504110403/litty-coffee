<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { ChatDotRound, Close } from '@element-plus/icons-vue'
import { crmApi, type ContactDetail, type Tag } from '@/api/crm'
import { intentLabels } from '@/api/automation'
import ContactAvatar from '@/components/ContactAvatar.vue'
import { useIsMobile } from '@/composables/useIsMobile'
import { fullDate } from '@/utils/format'
import { shortTime } from '@/utils/time'

const props = defineProps<{ contactId: number | null }>()
const emit = defineEmits<{ close: []; updated: [c: ContactDetail] }>()

const router = useRouter()
const isMobile = useIsMobile()
const open = computed({
  get: () => props.contactId != null,
  set: (v) => !v && emit('close'),
})

const detail = ref<ContactDetail | null>(null)
const allTags = ref<Tag[]>([])
const tagIds = ref<(number | string)[]>([])
const note = ref('')
const saving = ref(false)

watch(
  () => props.contactId,
  async (id) => {
    detail.value = null
    if (id == null) return
    const [d, tags] = await Promise.all([crmApi.contact(id), crmApi.tags()])
    detail.value = d
    allTags.value = tags
    tagIds.value = d.tags.map((t) => t.id)
    note.value = d.note
  },
  { immediate: true },
)

// el-select allow-create 會把新輸入的字串放進陣列 → 先建立標籤再換成 id
async function onTagsChange(vals: (number | string)[]) {
  const ids: number[] = []
  for (const v of vals) {
    if (typeof v === 'number') ids.push(v)
    else if (v.trim()) {
      const t = await crmApi.createTag(v.trim())
      if (!allTags.value.some((x) => x.id === t.id)) allTags.value.push(t)
      ids.push(t.id)
    }
  }
  const uniq = [...new Set(ids)]
  tagIds.value = uniq
  await save({ tagIds: uniq })
}

async function saveNote() {
  if (detail.value && note.value !== detail.value.note) await save({ note: note.value })
}

async function save(body: { note?: string; tagIds?: number[] }) {
  if (!detail.value) return
  saving.value = true
  try {
    detail.value = await crmApi.updateContact(detail.value.id, body)
    emit('updated', detail.value)
    if (body.note !== undefined) ElMessage.success('備註已儲存')
  } finally {
    saving.value = false
  }
}

function goChat() {
  if (detail.value?.conversationId) router.push({ name: 'inbox', params: { id: detail.value.conversationId } })
  emit('close')
}

const tagColor = (id: number | string) => allTags.value.find((t) => t.id === id)?.color ?? '#8b5e3c'
</script>

<template>
  <el-drawer
    v-model="open"
    :size="isMobile ? '85%' : '420px'"
    :direction="isMobile ? 'btt' : 'rtl'"
    :with-header="false"
    :class="{ 'sheet': isMobile }"
  >
    <div v-if="isMobile" class="grab" />
    <el-button class="close" text circle :icon="Close" aria-label="關閉" @click="emit('close')" />
    <div v-loading="!detail" class="drawer">
      <template v-if="detail">
        <div class="top">
          <ContactAvatar :contact="detail" :size="64" />
          <div class="who">
            <div class="name">{{ detail.name || detail.username || '匿名顧客' }}</div>
            <div v-if="detail.username" class="handle">@{{ detail.username }}</div>
          </div>
          <el-button v-if="detail.conversationId" type="primary" round :icon="ChatDotRound" @click="goChat">對話</el-button>
        </div>

        <div class="stats">
          <div>
            <b>{{ detail.inboundCount }}</b>
            <span>傳來訊息</span>
          </div>
          <div>
            <b>{{ detail.outboundByAuto }}</b>
            <span>自動回覆</span>
          </div>
          <div>
            <b>{{ detail.outboundByHuman }}</b>
            <span>真人回覆</span>
          </div>
        </div>

        <dl class="facts">
          <dt>第一次傳訊</dt>
          <dd>{{ fullDate(detail.firstSeenAt) }}</dd>
          <dt>最近傳訊</dt>
          <dd>{{ shortTime(detail.lastSeenAt) }}</dd>
          <dt>目前處理</dt>
          <dd>{{ detail.handler === 'human' ? '真人' : detail.handler === 'bot' ? 'AI 接待' : '—' }}</dd>
        </dl>

        <h4>標籤</h4>
        <el-select
          :model-value="tagIds"
          multiple
          filterable
          allow-create
          default-first-option
          :reserve-keyword="false"
          placeholder="輸入新增，例如：常客、帶狗、外帶"
          class="tag-select"
          @update:model-value="onTagsChange"
        >
          <template #tag>
            <span v-for="id in tagIds" :key="id" class="tag" :style="{ '--c': tagColor(id) }">
              {{ allTags.find((t) => t.id === id)?.name ?? id }}
            </span>
          </template>
          <el-option v-for="t in allTags" :key="t.id" :label="t.name" :value="t.id">
            <span class="dot" :style="{ background: t.color }" />{{ t.name }}
          </el-option>
        </el-select>

        <h4>備註</h4>
        <el-input
          v-model="note"
          type="textarea"
          :autosize="{ minRows: 3, maxRows: 8 }"
          placeholder="例如：喜歡燕麥拿鐵、生日 5 月"
          maxlength="500"
          @blur="saveNote"
        />

        <h4>常問的事</h4>
        <div v-if="detail.intents.length" class="intents">
          <span v-for="i in detail.intents" :key="i.intent" class="intent">
            {{ intentLabels[i.intent] ?? i.intent }} <b>{{ i.count }}</b>
          </span>
        </div>
        <p v-else class="muted">AI 回覆過才會累積</p>
      </template>
    </div>
  </el-drawer>
</template>

<style scoped lang="scss">
.drawer {
  min-height: 200px;
}
.close {
  position: absolute;
  top: 10px;
  right: 10px;
  z-index: 1;
}
.grab {
  width: 40px;
  height: 4px;
  border-radius: 2px;
  background: #ddd3c8;
  margin: -8px auto 14px;
}
:global(.el-drawer.sheet) {
  border-radius: 18px 18px 0 0;
}
:global(.el-drawer.sheet .el-drawer__body) {
  padding-bottom: calc(20px + env(safe-area-inset-bottom));
}
.top {
  display: flex;
  align-items: center;
  gap: 14px;
  padding-right: 36px;
}
.who {
  flex: 1;
  min-width: 0;
  .name {
    font-size: 18px;
    font-weight: 700;
  }
  .handle {
    color: var(--lc-muted);
    font-size: 13px;
  }
}
.stats {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
  margin: 18px 0 14px;
  div {
    background: var(--lc-cream);
    border-radius: 12px;
    padding: 10px;
    text-align: center;
  }
  b {
    display: block;
    font-size: 20px;
  }
  span {
    font-size: 12px;
    color: var(--lc-muted);
  }
}
.facts {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 6px 14px;
  margin: 0 0 6px;
  font-size: 13px;
  dt {
    color: var(--lc-muted);
  }
  dd {
    margin: 0;
  }
}
h4 {
  margin: 18px 0 8px;
  font-size: 14px;
}
.tag-select {
  width: 100%;
}
.tag {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 0 8px;
  margin: 2px 4px 2px 0;
  border-radius: 999px;
  font-size: 12px;
  line-height: 22px;
  background: color-mix(in srgb, var(--c) 14%, white);
  color: var(--lc-text);
  &::before {
    content: '';
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--c);
  }
}
.dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 8px;
}
.intents {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.intent {
  font-size: 13px;
  padding: 4px 10px;
  border-radius: 999px;
  background: var(--lc-cream);
  b {
    margin-left: 4px;
  }
}
.muted {
  font-size: 13px;
  color: var(--lc-muted);
  margin: 0;
}
</style>
