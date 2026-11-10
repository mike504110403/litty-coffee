<script setup lang="ts">
import { computed, ref } from 'vue'
import type { Contact } from '@/api/inbox'

const props = withDefaults(defineProps<{ contact: Contact; size?: number }>(), { size: 44 })

const failed = ref(false)
const label = computed(() => (props.contact.name || props.contact.username || '?').slice(0, 1).toUpperCase())

// 依 igsid 穩定挑一個暖色系底色
const palette = ['#c8915a', '#8b5e3c', '#a2504a', '#5f7f5a', '#6b6fa8', '#b07a9b', '#3f7f86']
const bg = computed(() => {
  let h = 0
  for (const ch of props.contact.igsid) h = (h * 31 + ch.charCodeAt(0)) >>> 0
  return palette[h % palette.length]
})
</script>

<template>
  <div class="avatar" :style="{ width: `${size}px`, height: `${size}px`, fontSize: `${size * 0.4}px`, background: bg }">
    <img v-if="contact.avatarUrl && !failed" :src="contact.avatarUrl" alt="" @error="failed = true" />
    <span v-else>{{ label }}</span>
  </div>
</template>

<style scoped>
.avatar {
  flex-shrink: 0;
  border-radius: 50%;
  display: grid;
  place-items: center;
  color: #fff;
  font-weight: 700;
  overflow: hidden;
}
img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}
</style>
