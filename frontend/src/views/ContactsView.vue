<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { Search } from '@element-plus/icons-vue'
import { crmApi, type CrmContact, type Tag } from '@/api/crm'
import { intentLabels } from '@/api/automation'
import ContactAvatar from '@/components/ContactAvatar.vue'
import ContactDrawer from '@/components/ContactDrawer.vue'
import { shortTime } from '@/utils/time'

const contacts = ref<CrmContact[]>([])
const tags = ref<Tag[]>([])
const loading = ref(true)
const q = ref('')
const tag = ref<number | null>(null)
const sort = ref<'recent' | 'active' | 'new'>('recent')
const selected = ref<number | null>(null)

async function load() {
  loading.value = true
  try {
    const [list, t] = await Promise.all([
      crmApi.contacts({ q: q.value.trim() || undefined, tag: tag.value ?? undefined, sort: sort.value }),
      crmApi.tags(),
    ])
    contacts.value = list
    tags.value = t
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch([tag, sort], load)
let timer: ReturnType<typeof setTimeout> | undefined
watch(q, () => {
  clearTimeout(timer)
  timer = setTimeout(load, 300)
})

function onUpdated(c: CrmContact) {
  const i = contacts.value.findIndex((x) => x.id === c.id)
  if (i >= 0) contacts.value[i] = { ...contacts.value[i], ...c }
  void crmApi.tags().then((t) => (tags.value = t))
}
</script>

<template>
  <div>
    <div class="head">
      <div>
        <h1 class="lc-page-title">顧客</h1>
        <p class="lc-page-sub">傳過私訊的 IG 用戶；可加標籤、寫備註，方便認出常客</p>
      </div>
    </div>

    <div class="filters">
      <el-input v-model="q" :prefix-icon="Search" placeholder="搜尋名稱、帳號或備註" clearable class="search" />
      <el-select v-model="sort" class="sort">
        <el-option label="最近互動" value="recent" />
        <el-option label="訊息最多" value="active" />
        <el-option label="最新加入" value="new" />
      </el-select>
    </div>
    <div v-if="tags.length" class="tag-filter">
      <button class="chip" :class="{ on: tag == null }" @click="tag = null">全部</button>
      <button
        v-for="t in tags"
        :key="t.id"
        class="chip"
        :class="{ on: tag === t.id }"
        :style="{ '--c': t.color }"
        @click="tag = tag === t.id ? null : t.id"
      >
        <i />{{ t.name }} <span>{{ t.count }}</span>
      </button>
    </div>

    <div v-loading="loading" class="list lc-card">
      <button v-for="c in contacts" :key="c.id" class="row" @click="selected = c.id">
        <ContactAvatar :contact="c" :size="42" />
        <div class="main">
          <div class="line1">
            <span class="name">{{ c.name || c.username || '匿名顧客' }}</span>
            <span v-if="c.username && c.name" class="handle">@{{ c.username }}</span>
          </div>
          <div class="line2">
            <span v-for="t in c.tags" :key="t.id" class="tag" :style="{ '--c': t.color }">{{ t.name }}</span>
            <span v-if="c.note" class="note">{{ c.note }}</span>
          </div>
        </div>
        <div class="meta">
          <span class="when">{{ shortTime(c.lastSeenAt) }}</span>
          <span class="count">{{ c.inboundCount }} 則<template v-if="c.lastIntent"> · {{ intentLabels[c.lastIntent] ?? c.lastIntent }}</template></span>
        </div>
      </button>
      <el-empty v-if="!loading && !contacts.length" :image-size="90" description="沒有符合的顧客" />
    </div>

    <ContactDrawer :contact-id="selected" @close="selected = null" @updated="onUpdated" />
  </div>
</template>

<style scoped lang="scss">
.filters {
  display: flex;
  gap: 10px;
  margin-bottom: 10px;
  .search {
    max-width: 360px;
  }
  .search {
    flex: 1;
  }
  .sort {
    width: 124px;
    flex-shrink: 0;
  }
}
.tag-filter {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 14px;
}
.chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 12px;
  border: 1px solid var(--lc-border);
  border-radius: 999px;
  background: #fff;
  font: inherit;
  font-size: 13px;
  cursor: pointer;
  i {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--c);
  }
  span {
    color: var(--lc-muted);
    font-size: 12px;
  }
  &.on {
    border-color: var(--lc-espresso);
    background: var(--lc-espresso);
    color: #fff;
    span {
      color: var(--lc-latte);
    }
  }
}
.list {
  overflow: hidden;
  min-height: 120px;
}
.row {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 12px 16px;
  border: 0;
  border-bottom: 1px solid var(--lc-border);
  background: transparent;
  text-align: left;
  font: inherit;
  color: inherit;
  cursor: pointer;
  &:last-child {
    border-bottom: 0;
  }
  &:hover {
    background: #faf6f1;
  }
}
.main {
  flex: 1;
  min-width: 0;
}
.line1 {
  display: flex;
  align-items: baseline;
  gap: 8px;
  .name {
    font-weight: 500;
  }
  .handle {
    font-size: 12px;
    color: var(--lc-muted);
  }
}
.line2 {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 4px;
  min-width: 0;
  overflow: hidden;
}
.tag {
  flex-shrink: 0;
  font-size: 11px;
  padding: 1px 8px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--c) 14%, white);
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--c) 35%, white);
}
.note {
  font-size: 12px;
  color: var(--lc-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.meta {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 4px;
  font-size: 12px;
  color: var(--lc-muted);
  flex-shrink: 0;
}
@media (max-width: 768px) {
  .head .lc-page-title {
    display: none;
  }
  .filters .search {
    max-width: none;
  }
  .list {
    margin: 0 -16px;
    border-radius: 0;
    border-left: 0;
    border-right: 0;
  }
  .row {
    padding: 12px 16px;
  }
}
</style>
