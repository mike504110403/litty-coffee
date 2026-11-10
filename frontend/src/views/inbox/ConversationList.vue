<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Search } from '@element-plus/icons-vue'
import { contactLabel, type Conversation } from '@/api/inbox'
import ContactAvatar from '@/components/ContactAvatar.vue'
import { shortTime } from '@/utils/time'

const props = defineProps<{ conversations: Conversation[]; activeId: number | null; loading: boolean }>()
const emit = defineEmits<{ select: [id: number]; search: [q: string] }>()

type Tab = 'all' | 'unread' | 'human' | 'closed'
const tab = ref<Tab>('all')
const tabs: { key: Tab; label: string }[] = [
  { key: 'all', label: '全部' },
  { key: 'unread', label: '未讀' },
  { key: 'human', label: '真人處理' },
  { key: 'closed', label: '已結束' },
]

const q = ref('')
let timer: ReturnType<typeof setTimeout> | undefined
watch(q, (v) => {
  clearTimeout(timer)
  timer = setTimeout(() => emit('search', v.trim()), 300)
})

const filtered = computed(() =>
  props.conversations.filter((c) => {
    switch (tab.value) {
      case 'unread':
        return c.unreadCount > 0
      case 'human':
        return c.status === 'open' && c.handler === 'human'
      case 'closed':
        return c.status === 'closed'
      default:
        return c.status === 'open'
    }
  }),
)

const count = (k: Tab) =>
  k === 'unread' ? props.conversations.filter((c) => c.unreadCount > 0).length : 0
</script>

<template>
  <div class="list">
    <div class="head">
      <el-input v-model="q" placeholder="搜尋顧客或訊息" :prefix-icon="Search" clearable />
      <div class="tabs">
        <button v-for="t in tabs" :key="t.key" class="tab" :class="{ on: tab === t.key }" @click="tab = t.key">
          {{ t.label }}
          <span v-if="count(t.key)" class="tab-count">{{ count(t.key) }}</span>
        </button>
      </div>
    </div>

    <div v-loading="loading && !conversations.length" class="items">
      <button
        v-for="c in filtered"
        :key="c.id"
        class="item"
        :class="{ active: c.id === activeId, unread: c.unreadCount > 0 }"
        @click="emit('select', c.id)"
      >
        <ContactAvatar :contact="c.contact" />
        <div class="body">
          <div class="row1">
            <span class="name">{{ contactLabel(c.contact) }}</span>
            <span class="time">{{ shortTime(c.lastMessageAt) }}</span>
          </div>
          <div class="row2">
            <span class="preview">{{ c.lastMessageText || '（無文字內容）' }}</span>
            <span v-if="c.unreadCount" class="badge">{{ c.unreadCount > 99 ? '99+' : c.unreadCount }}</span>
          </div>
          <div class="row3">
            <span class="chip" :class="c.handler">{{ c.handler === 'human' ? '真人處理' : 'AI 接待' }}</span>
            <span v-if="c.contact.username && c.contact.name" class="handle">@{{ c.contact.username }}</span>
          </div>
        </div>
      </button>

      <el-empty v-if="!loading && !filtered.length" :image-size="90" description="這裡還沒有對話" />
    </div>
  </div>
</template>

<style scoped lang="scss">
.list {
  display: flex;
  flex-direction: column;
  min-height: 0;
  height: 100%;
}
.head {
  padding: 14px 14px 8px;
  border-bottom: 1px solid var(--lc-border);
}
.tabs {
  display: flex;
  gap: 6px;
  margin-top: 10px;
  overflow-x: auto;
  scrollbar-width: none;
}
.tab {
  flex-shrink: 0;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  border: 0;
  padding: 6px 12px;
  border-radius: 999px;
  background: transparent;
  color: var(--lc-muted);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
  &.on {
    background: var(--lc-espresso);
    color: #fff;
  }
}
.tab-count {
  font-size: 11px;
  padding: 0 6px;
  border-radius: 999px;
  background: var(--lc-caramel);
  color: #fff;
}
.items {
  flex: 1;
  overflow-y: auto;
  padding: 6px;
}
.item {
  width: 100%;
  display: flex;
  gap: 12px;
  align-items: flex-start;
  padding: 12px 10px;
  border: 0;
  border-radius: 14px;
  background: transparent;
  text-align: left;
  font: inherit;
  color: inherit;
  cursor: pointer;
  transition: background 0.12s;
  &:hover {
    background: #f7f1ea;
  }
  &.active {
    background: var(--el-color-primary-light-9);
    box-shadow: inset 3px 0 0 var(--el-color-primary);
  }
}
.body {
  flex: 1;
  min-width: 0;
}
.row1,
.row2 {
  display: flex;
  align-items: center;
  gap: 8px;
}
.name {
  flex: 1;
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.time {
  font-size: 12px;
  color: var(--lc-muted);
}
.row2 {
  margin-top: 3px;
}
.preview {
  flex: 1;
  font-size: 13px;
  color: var(--lc-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.unread .preview {
  color: var(--lc-text);
  font-weight: 500;
}
.badge {
  min-width: 20px;
  height: 20px;
  padding: 0 6px;
  border-radius: 999px;
  background: #d9534f;
  color: #fff;
  font-size: 11px;
  font-weight: 700;
  display: grid;
  place-items: center;
}
.row3 {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 6px;
}
.chip {
  font-size: 11px;
  padding: 1px 8px;
  border-radius: 999px;
  &.bot {
    background: #eef3ec;
    color: #4f7a4a;
  }
  &.human {
    background: #f6e6d4;
    color: #a86a2c;
  }
}
.handle {
  font-size: 12px;
  color: var(--lc-muted);
}
</style>
