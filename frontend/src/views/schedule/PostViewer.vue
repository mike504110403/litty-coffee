<script setup lang="ts">
import { computed } from 'vue'
import { Link } from '@element-plus/icons-vue'
import { kindLabels, type Post } from '@/api/posts'
import AccountTag from '@/components/AccountTag.vue'
import { useAccountsStore } from '@/stores/accounts'
import { useIsMobile } from '@/composables/useIsMobile'
import PostInsights from './PostInsights.vue'

// 從 IG 同步進來的貼文（source='ig'）：唯讀檢視，不能改、刪、重排程
const props = defineProps<{ post: Post | null }>()
const visible = defineModel<boolean>({ required: true })
const emit = defineEmits<{ insightsUpdated: [post: Post] }>()

const isMobile = useIsMobile()
const accounts = useAccountsStore()

const when = computed(() => {
  const iso = props.post?.publishedAt ?? props.post?.scheduledAt
  if (!iso) return '—'
  const d = new Date(iso)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()} ${pad(d.getHours())}:${pad(d.getMinutes())}`
})
const kindText = computed(() => {
  const p = props.post
  if (!p) return ''
  return p.kind === 'carousel' ? '輪播貼文' : kindLabels[p.kind]
})
</script>

<template>
  <el-dialog
    v-model="visible"
    :title="`${kindText} · IG 發佈`"
    :fullscreen="isMobile"
    width="560px"
    top="6vh"
    destroy-on-close
    class="viewer"
  >
    <div v-if="post" class="body">
      <div v-if="post.igThumb" class="thumb"><img :src="post.igThumb" alt="" /></div>
      <dl class="meta">
        <template v-if="accounts.multi">
          <dt>帳號</dt>
          <dd><AccountTag :id="post.igAccountId" /></dd>
        </template>
        <dt>類型</dt>
        <dd>{{ kindText }}</dd>
        <dt>發佈時間</dt>
        <dd>{{ when }}</dd>
        <dt>IG 連結</dt>
        <dd>
          <a v-if="post.permalink" :href="post.permalink" target="_blank" rel="noopener noreferrer" class="link">
            <el-icon><Link /></el-icon>在 IG 開啟
          </a>
          <span v-else>—</span>
        </dd>
      </dl>
      <section class="cap-box">
        <h4>文案</h4>
        <p v-if="post.caption" class="cap">{{ post.caption }}</p>
        <p v-else class="cap none">（無文案）</p>
      </section>
      <section>
        <h4>成效</h4>
        <PostInsights :post="post" @updated="(p) => emit('insightsUpdated', p)" />
      </section>
    </div>
  </el-dialog>
</template>

<style scoped lang="scss">
.body {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.thumb {
  border-radius: 12px;
  overflow: hidden;
  background: var(--lc-bg-thumb);
  img {
    display: block;
    width: 100%;
    max-height: 300px;
    object-fit: contain;
    margin: 0 auto;
  }
}
.meta {
  display: grid;
  grid-template-columns: 76px 1fr;
  gap: 8px 12px;
  margin: 0;
  font-size: 14px;
  dt {
    color: var(--lc-muted);
  }
  dd {
    margin: 0;
    min-width: 0;
  }
}
.link {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-height: 24px;
  color: var(--el-color-primary);
  text-decoration: none;
}
h4 {
  margin: 0 0 6px;
  font-size: 13px;
  color: var(--lc-muted);
  font-weight: 500;
}
.cap {
  margin: 0;
  font-size: 14px;
  line-height: 1.65;
  white-space: pre-wrap;
  word-break: break-word;
  &.none {
    color: var(--lc-muted);
  }
}
.cap-box {
  padding: 12px 14px;
  border-radius: 12px;
  background: var(--lc-bg-soft);
  border: 1px solid var(--lc-border);
}

// 手機全螢幕：標題固定，內容區單一捲動
:global(.el-dialog.viewer.is-fullscreen) {
  display: flex;
  flex-direction: column;
  height: 100%;
  overflow: hidden;
  padding: 0;
}
@supports (height: 100dvh) {
  :global(.el-dialog.viewer.is-fullscreen) {
    height: 100dvh;
  }
}
:global(.el-dialog.viewer.is-fullscreen .el-dialog__header) {
  flex: none;
  padding: 14px 16px 10px;
  border-bottom: 1px solid var(--lc-border);
}
:global(.el-dialog.viewer.is-fullscreen .el-dialog__headerbtn) {
  top: 0;
  right: 0;
  width: 48px;
  height: 52px;
}
:global(.el-dialog.viewer.is-fullscreen .el-dialog__body) {
  flex: 1;
  min-height: 0;
  padding: 16px 16px calc(24px + env(safe-area-inset-bottom));
  overflow-y: auto;
  overscroll-behavior: contain;
  -webkit-overflow-scrolling: touch;
}
@media (max-width: 768px) {
  .meta {
    font-size: 15px;
  }
  .link {
    min-height: 40px;
  }
}
</style>
