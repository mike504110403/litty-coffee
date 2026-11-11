<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { auditApi, type AuditAction, type AuditAdmin, type AuditLog } from '@/api/audit'
import { useAccountsStore } from '@/stores/accounts'
import { useIsMobile } from '@/composables/useIsMobile'

// 操作紀錄：誰、何時、從哪、做了什麼、成功與否。篩選：管理員、動作、日期區間（多帳號時另可篩帳號）
const accounts = useAccountsStore()
const isMobile = useIsMobile() // 手機日期欄位不可打字，避免彈出鍵盤

const filters = reactive({
  admin: null as number | null,
  action: '',
  account: null as number | null,
  range: null as [Date, Date] | null,
})
const page = ref(1)
const pageSize = ref(50)
const total = ref(0)
const items = ref<AuditLog[]>([])
const loading = ref(false)
const actions = ref<AuditAction[]>([])
const admins = ref<AuditAdmin[]>([])

function dayStart(d: Date): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate())
}

async function load() {
  loading.value = true
  try {
    const [start, end] = filters.range ?? [null, null]
    const res = await auditApi.list({
      admin: filters.admin ?? undefined,
      action: filters.action || undefined,
      account: filters.account ?? undefined,
      // 日期區間以瀏覽器時區切日；結束日含當天
      from: start ? dayStart(start).toISOString() : undefined,
      to: end ? new Date(dayStart(end).getTime() + 86400000).toISOString() : undefined,
      page: page.value,
      pageSize: pageSize.value,
    })
    items.value = res.items
    total.value = res.total
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  void accounts.ensure()
  const [a, b] = await Promise.all([auditApi.actions(), auditApi.admins()])
  actions.value = a
  admins.value = b
})
onMounted(load)

watch(
  () => [filters.admin, filters.action, filters.account, filters.range],
  () => {
    if (page.value !== 1) page.value = 1
    else void load()
  },
)
watch([page, pageSize], load)

const pad = (n: number) => String(n).padStart(2, '0')
function fullTime(iso: string): string {
  const d = new Date(iso)
  return `${d.getFullYear()}/${pad(d.getMonth() + 1)}/${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

// el-table 的 slot row 型別是 DefaultRow，轉回 AuditLog
const asLog = (r: unknown) => r as AuditLog
const resultLabel = (l: AuditLog) => (l.success ? '成功' : l.status ? `失敗（${l.status}）` : '失敗')
const accountName = (id: number | null) => (id == null ? '' : accounts.label(id))
const detailText = (l: AuditLog) => JSON.stringify(l.detail, null, 2)
const showAccount = computed(() => accounts.multi)
</script>

<template>
  <div class="audit">
    <div class="filters">
      <el-select v-model="filters.admin" placeholder="全部管理員" clearable filterable class="f-admin">
        <el-option
          v-for="a in admins"
          :key="a.id"
          :label="a.disabled ? `${a.displayName}（已停用）` : a.displayName"
          :value="a.id"
        />
      </el-select>
      <el-select v-model="filters.action" placeholder="全部動作" clearable filterable class="f-action">
        <el-option v-for="a in actions" :key="a.key" :label="a.name" :value="a.key" />
      </el-select>
      <el-select
        v-if="showAccount"
        v-model="filters.account"
        placeholder="全部帳號"
        clearable
        class="f-account"
      >
        <el-option v-for="a in accounts.list" :key="a.id" :label="accounts.label(a.id)" :value="a.id" />
      </el-select>
      <el-date-picker
        v-model="filters.range"
        type="daterange"
        range-separator="～"
        start-placeholder="開始日期"
        end-placeholder="結束日期"
        :editable="!isMobile"
        popper-class="lc-picker-popper"
        class="f-range"
      />
      <el-button :icon="Refresh" :loading="loading" @click="load">重新整理</el-button>
    </div>

    <el-table v-loading="loading" :data="items" row-key="id" class="table" empty-text="沒有符合的紀錄">
      <el-table-column type="expand">
        <template #default="{ row }">
          <div class="detail">
            <dl>
              <dt>說明</dt>
              <dd>{{ row.summary }}</dd>
              <template v-if="row.igAccountId != null">
                <dt>IG 帳號</dt>
                <dd>{{ accountName(row.igAccountId) }}</dd>
              </template>
              <dt>動作鍵</dt>
              <dd class="mono">{{ row.action }}</dd>
              <dt>裝置</dt>
              <dd class="ua">{{ row.userAgent || '—' }}</dd>
            </dl>
            <pre class="json">{{ detailText(asLog(row)) }}</pre>
          </div>
        </template>
      </el-table-column>
      <el-table-column label="時間" width="170">
        <template #default="{ row }">{{ fullTime(row.createdAt) }}</template>
      </el-table-column>
      <el-table-column label="管理員" min-width="110">
        <template #default="{ row }">{{ row.adminName || '—' }}</template>
      </el-table-column>
      <el-table-column label="動作" min-width="150">
        <template #default="{ row }">{{ row.actionName }}</template>
      </el-table-column>
      <el-table-column label="對象" min-width="140">
        <template #default="{ row }">
          <span>{{ row.targetLabel || '—' }}</span>
          <span v-if="showAccount && row.igAccountId != null" class="acc">{{ accountName(row.igAccountId) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="結果" width="110">
        <template #default="{ row }">
          <el-tag :type="row.success ? 'success' : 'danger'" size="small" disable-transitions>
            {{ resultLabel(asLog(row)) }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="IP" min-width="120">
        <template #default="{ row }"><span class="mono">{{ row.ip || '—' }}</span></template>
      </el-table-column>
    </el-table>

    <el-pagination
      v-model:current-page="page"
      v-model:page-size="pageSize"
      :total="total"
      :page-sizes="[20, 50, 100, 200]"
      layout="total, sizes, prev, pager, next"
      class="pager"
      size="small"
      background
    />
  </div>
</template>

<style scoped>
.audit {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.filters {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}
.f-admin,
.f-account {
  width: 160px;
}
.f-action {
  width: 200px;
}
.f-range {
  max-width: 280px;
}
.table {
  width: 100%;
}
.detail {
  padding: 4px 16px 8px 48px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.detail dl {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 4px 12px;
  margin: 0;
}
.detail dt {
  color: var(--el-text-color-secondary);
}
.detail dd {
  margin: 0;
  word-break: break-all;
}
.json {
  margin: 0;
  padding: 8px 12px;
  border-radius: 6px;
  background: var(--el-fill-color-light);
  color: var(--el-text-color-regular);
  font-size: 12px;
  max-height: 320px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-all;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
.ua {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.acc {
  margin-left: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.pager {
  justify-content: flex-end;
  flex-wrap: wrap;
}
@media (max-width: 640px) {
  .f-admin,
  .f-action,
  .f-account,
  .f-range {
    width: 100%;
    max-width: none;
  }
  .detail {
    padding-left: 8px;
  }
}
</style>
