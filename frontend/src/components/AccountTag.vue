<script setup lang="ts">
import { computed } from 'vue'
import { useAccountsStore } from '@/stores/accounts'
import AccountAvatar from './AccountAvatar.vue'

// 標示資料屬於哪個我方 IG 帳號（小頭像＋帳號名）；compact 只顯示頭像
const props = defineProps<{ id: number | null | undefined; compact?: boolean }>()
const accounts = useAccountsStore()
const account = computed(() => (props.id == null ? null : (accounts.byId.get(props.id) ?? null)))
</script>

<template>
  <span class="acc-tag" :class="{ compact }" :title="accounts.label(id)">
    <AccountAvatar :account="account" :size="compact ? 16 : 18" />
    <span v-if="!compact" class="name">{{ accounts.label(id) }}</span>
  </span>
</template>

<style scoped>
.acc-tag {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  max-width: 100%;
  padding: 1px 8px 1px 2px;
  border-radius: 999px;
  background: var(--lc-bg-chip);
  color: var(--lc-muted);
  font-size: 12px;
  line-height: 18px;
  vertical-align: middle;
}
.acc-tag.compact {
  padding: 0;
  background: transparent;
}
.name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
@media (max-width: 768px) {
  .acc-tag {
    font-size: 13px;
    line-height: 20px;
  }
}
</style>
