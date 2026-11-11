<script setup lang="ts">
import { computed, ref } from 'vue'
import type { IgAccount } from '@/api/accounts'

// 我方 IG 帳號的小頭像（沒有頭像時顯示帳號首字）
const props = withDefaults(defineProps<{ account?: IgAccount | null; size?: number }>(), { size: 20 })
const failed = ref(false)
const letter = computed(() => (props.account?.username || props.account?.name || '?').slice(0, 1).toUpperCase())
</script>

<template>
  <span class="acc-avatar" :style="{ width: `${size}px`, height: `${size}px`, fontSize: `${size * 0.5}px` }">
    <img v-if="account?.profilePic && !failed" :src="account.profilePic" alt="" @error="failed = true" />
    <span v-else>{{ letter }}</span>
  </span>
</template>

<style scoped>
.acc-avatar {
  flex-shrink: 0;
  display: inline-grid;
  place-items: center;
  border-radius: 50%;
  overflow: hidden;
  background: var(--lc-tone-caramel-bg);
  color: var(--lc-tone-caramel-fg);
  font-weight: 700;
  line-height: 1;
}
img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}
</style>
