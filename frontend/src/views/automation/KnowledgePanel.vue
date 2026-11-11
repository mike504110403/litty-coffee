<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Delete } from '@element-plus/icons-vue'
import { automationApi, type Knowledge } from '@/api/automation'

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
    items.value = await automationApi.knowledge()
    dirty.value = new Set()
  } finally {
    loading.value = false
  }
}
onMounted(load)

const markDirty = (k: Knowledge) => dirty.value.add(k.id)

async function save(k: Knowledge) {
  saving.value = k.id
  try {
    Object.assign(k, await automationApi.saveKnowledge(k))
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
  items.value.push(await automationApi.saveKnowledge({ title: value.trim(), content: '', enabled: true, sort }))
}

async function remove(k: Knowledge) {
  if (!(await ElMessageBox.confirm(`刪除「${k.title}」？`, '刪除段落', { type: 'warning', confirmButtonText: '刪除', cancelButtonText: '取消' }).catch(() => false))) return
  await automationApi.deleteKnowledge(k.id)
  items.value = items.value.filter((x) => x.id !== k.id)
}
</script>

<template>
  <div>
    <div class="intro lc-card">
      AI 只會根據這裡的內容回答。寫得越具體（價格、時間、規則），AI 越不需要轉真人。沒填的段落不會給 AI 看。
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
  </div>
</template>

<style scoped lang="scss">
.intro {
  padding: 14px 16px;
  font-size: 14px;
  line-height: 1.6;
  color: var(--lc-roast);
  background: var(--lc-warn-bg-2);
  border-color: var(--lc-warn-line);
  margin-bottom: 14px;
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
@media (max-width: 768px) {
  .list {
    grid-template-columns: 1fr;
  }
}
</style>
