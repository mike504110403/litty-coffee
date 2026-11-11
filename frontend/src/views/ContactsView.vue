<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { Search } from '@element-plus/icons-vue'
import { crmApi, type CrmContact, type Tag } from '@/api/crm'
import { intentLabels } from '@/api/automation'
import ContactAvatar from '@/components/ContactAvatar.vue'
import ContactDrawer from '@/components/ContactDrawer.vue'
import AccountSelect from '@/components/AccountSelect.vue'
import AccountTag from '@/components/AccountTag.vue'
import RiskTag from '@/components/RiskTag.vue'
import type { Risk } from '@/api/inbox'
import { useAccountsStore } from '@/stores/accounts'
import { shortTime } from '@/utils/time'

const contacts = ref<CrmContact[]>([])
const tags = ref<Tag[]>([])
const loading = ref(true)
const q = ref('')
const tag = ref<number | null>(null)
const risk = ref<Exclude<Risk, ''> | null>(null) // 黑名單／問題顧客篩選
const sort = ref<'recent' | 'active' | 'new'>('recent')
const selected = ref<number | null>(null)
// 帳號篩選（null＝全部帳號）；標籤數也只算該帳號的顧客
const accounts = useAccountsStore()
const account = ref<number | null>(null)
onMounted(() => void accounts.ensure())

async function load() {
  loading.value = true
  try {
    const [list, t] = await Promise.all([
      crmApi.contacts({
        q: q.value.trim() || undefined,
        tag: tag.value ?? undefined,
        risk: risk.value ?? undefined,
        sort: sort.value,
        account: account.value ?? undefined,
      }),
      crmApi.tags(account.value ?? undefined),
    ])
    contacts.value = list
    tags.value = t
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch([tag, sort, account, risk], load)
let timer: ReturnType<typeof setTimeout> | undefined
watch(q, () => {
  clearTimeout(timer)
  timer = setTimeout(load, 300)
})

function onUpdated(c: CrmContact) {
  const i = contacts.value.findIndex((x) => x.id === c.id)
  if (i >= 0) contacts.value[i] = { ...contacts.value[i], ...c }
  // 依標記篩選時，改掉標記的就移出清單
  if (risk.value && c.risk !== risk.value) contacts.value = contacts.value.filter((x) => x.id !== c.id)
  void crmApi.tags(account.value ?? undefined).then((t) => (tags.value = t))
}
</script>

<template>
  <div class="lc-m">
    <div class="head">
      <div>
        <h1 class="lc-page-title lc-page-title--solo">顧客</h1>
      </div>
    </div>

    <div class="filters">
      <el-input v-model="q" :prefix-icon="Search" placeholder="搜尋名稱、帳號或備註" clearable class="search" />
      <el-select v-model="sort" class="sort">
        <el-option label="最近互動" value="recent" />
        <el-option label="訊息最多" value="active" />
        <el-option label="最新加入" value="new" />
      </el-select>
      <AccountSelect v-if="accounts.multi" v-model="account" allow-all class="acc-filter" />
    </div>
    <div class="tag-filter">
      <button class="chip" :class="{ on: tag == null && risk == null }" @click="(tag = null), (risk = null)">全部</button>
      <button class="chip risk blocked" :class="{ on: risk === 'blocked' }" @click="risk = risk === 'blocked' ? null : 'blocked'">
        <i />黑名單
      </button>
      <button class="chip risk watch" :class="{ on: risk === 'watch' }" @click="risk = risk === 'watch' ? null : 'watch'">
        <i />問題顧客
      </button>
      <span v-if="tags.length" class="sep" />
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
            <RiskTag :risk="c.risk" :reason="c.riskReason" :source="c.riskSource" />
          </div>
          <div class="line2">
            <span v-for="t in c.tags" :key="t.id" class="tag" :style="{ '--c': t.color }">{{ t.name }}</span>
            <span v-if="c.note" class="note">{{ c.note }}</span>
          </div>
        </div>
        <div class="meta">
          <span class="when">{{ shortTime(c.lastSeenAt) }}</span>
          <span class="count">{{ c.inboundCount }} 則<template v-if="c.lastIntent"> · {{ intentLabels[c.lastIntent] ?? c.lastIntent }}</template></span>
          <AccountTag v-if="accounts.multi" :id="c.igAccountId" />
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
  flex-wrap: wrap;
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
  .acc-filter {
    width: 160px;
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
  background: var(--lc-surface);
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
    color: var(--lc-on-fill);
    span {
      color: var(--lc-latte);
    }
  }
}
.chip.risk.blocked i {
  background: var(--lc-fail-fg);
}
.chip.risk.watch i {
  background: var(--lc-warn-fg);
}
.sep {
  width: 1px;
  margin: 4px 2px;
  background: var(--lc-border);
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
    background: var(--lc-bg-hover-2);
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
  background: color-mix(in srgb, var(--c) 14%, var(--lc-tag-mix));
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--c) 35%, var(--lc-tag-mix));
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
@media (min-width: 1024px) {
  // 桌機：每位顧客一張卡片、兩欄並排，不拉成一條長橫列
  .list {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 12px;
    align-items: start;
    overflow: visible;
    background: none;
    border: 0;
    box-shadow: none;
    :deep(.el-empty) {
      grid-column: 1 / -1;
    }
  }
  .row {
    background: var(--lc-surface);
    border: 1px solid var(--lc-border);
    border-radius: var(--lc-radius);
    box-shadow: var(--lc-shadow);
    &:hover {
      background: var(--lc-bg-hover-2);
    }
  }
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
    min-height: 64px;
  }
  .filters {
    gap: 8px;
    .search {
      flex: 1 1 100%; // 搜尋獨佔一列，排序／帳號在下一列
    }
    .sort {
      flex: 1 1 0;
      width: auto;
    }
    .acc-filter {
      flex: 1 1 0;
      width: auto;
    }
  }
  // 篩選標籤：一列可橫滑（不換行佔掉一屏），點擊範圍 ≥40px
  .tag-filter {
    flex-wrap: nowrap;
    overflow-x: auto;
    margin: 0 -16px 12px;
    padding: 0 16px;
    scrollbar-width: none;
    -webkit-overflow-scrolling: touch;
    overscroll-behavior-x: contain;
    &::-webkit-scrollbar {
      display: none;
    }
  }
  .chip {
    flex-shrink: 0;
    min-height: 40px;
    padding: 0 14px;
    font-size: 14px;
    white-space: nowrap;
    span {
      font-size: 13px;
    }
  }
  .sep {
    flex-shrink: 0;
  }
  .line1 {
    flex-wrap: wrap;
    row-gap: 2px;
    .name {
      flex-shrink: 0;
      max-width: 100%;
    }
  }
  .line1 .handle,
  .meta,
  .note {
    font-size: 13px;
  }
  .tag {
    font-size: 13px;
    padding: 1px 9px;
  }
  .meta {
    max-width: 38%;
  }
}
</style>
