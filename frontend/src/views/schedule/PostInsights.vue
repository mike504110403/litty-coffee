<script setup lang="ts">
import { computed, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { insightKeysOf, insightLabels, postsApi, type Post } from '@/api/posts'
import { formatCount } from '@/utils/format'

// 已發佈貼文的 IG 成效：數字列＋取得時間＋「更新數據」
const props = defineProps<{ post: Post; compact?: boolean }>()
const emit = defineEmits<{ updated: [post: Post] }>()

const keys = computed(() => insightKeysOf(props.post.kind))
const has = computed(() => !!props.post.insightsFetchedAt)
const refreshing = ref(false)

const fetchedLabel = computed(() => {
  const at = props.post.insightsFetchedAt
  if (!at) return props.post.insightsError ? '尚未取得數據' : '尚未取得數據，稍後會自動更新'
  const d = new Date(at)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getMonth() + 1}/${d.getDate()} ${pad(d.getHours())}:${pad(d.getMinutes())} 更新`
})

async function refresh() {
  refreshing.value = true
  try {
    emit('updated', await postsApi.refreshInsights(props.post.id))
    ElMessage.success('數據已更新')
  } catch {
    // 錯誤訊息已由 http 攔截器顯示
  } finally {
    refreshing.value = false
  }
}
</script>

<template>
  <div class="insights" :class="{ compact }">
    <ul v-if="has" class="stats">
      <li v-for="k in keys" :key="k">
        <b>{{ props.post.insights[k] == null ? '—' : formatCount(props.post.insights[k]!) }}</b>
        <span>{{ insightLabels[k] }}</span>
      </li>
    </ul>
    <div class="foot">
      <span class="when">{{ fetchedLabel }}</span>
      <span v-if="post.insightsError" class="err" :title="post.insightsError">上次更新失敗</span>
      <el-button
        v-if="post.insightsRefreshable"
        size="small"
        text
        type="primary"
        :icon="Refresh"
        :loading="refreshing"
        @click.stop="refresh"
      >
        更新數據
      </el-button>
      <span v-else class="when">限動數據僅 24 小時內可更新</span>
    </div>
  </div>
</template>

<style scoped lang="scss">
.insights {
  margin-top: 8px;
}
.stats {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  grid-template-columns: repeat(6, minmax(0, 1fr));
  gap: 8px;
  li {
    display: flex;
    flex-direction: column;
    padding: 8px 10px;
    border-radius: 10px;
    background: var(--lc-cream);
  }
  b {
    font-size: 18px;
    font-variant-numeric: tabular-nums;
  }
  span {
    font-size: 12px;
    color: var(--lc-muted);
  }
}
.foot {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  margin-top: 6px;
  font-size: 12px;
  .when {
    color: var(--lc-muted);
  }
  .err {
    color: #b42318;
  }
}
.compact {
  margin-top: 6px;
  .stats {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 14px;
    li {
      flex-direction: row;
      align-items: baseline;
      gap: 4px;
      padding: 0;
      background: none;
    }
    b {
      font-size: 13px;
    }
  }
  .foot {
    margin-top: 2px;
  }
}
@media (max-width: 768px) {
  .stats {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}
</style>
