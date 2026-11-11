<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { MediaAsset, PostKind } from '@/api/posts'

// 仿 IG 版面的預覽；不追求像素級一致，只讓店家看出裁切與文案斷行
// username／avatar 是要發佈的那個 IG 帳號
const props = defineProps<{ kind: PostKind; media: MediaAsset[]; caption: string; username?: string; avatar?: string }>()
const avatarFailed = ref(false)
watch(
  () => props.avatar,
  () => (avatarFailed.value = false),
)
const avatarSrc = computed(() => (props.avatar && !avatarFailed.value ? props.avatar : '/favicon.svg'))

const idx = ref(0)
watch(
  () => props.media.length,
  (n) => {
    if (idx.value >= n) idx.value = Math.max(0, n - 1)
  },
)
const current = computed(() => props.media[idx.value])
const expanded = ref(false)

const ratio = computed(() => {
  if (props.kind === 'reel' || props.kind === 'story') return '9 / 16'
  const a = props.media[0]
  if (!a || !a.width || !a.height) return '1 / 1'
  const r = Math.min(1.91, Math.max(0.8, a.width / a.height)) // IG 會裁到 4:5～1.91:1
  return `${r}`
})
</script>

<template>
  <div class="ig" :class="kind">
    <div v-if="kind !== 'story'" class="ig-head">
      <img :src="avatarSrc" alt="" class="ig-avatar" @error="avatarFailed = true" />
      <strong>{{ username || 'littycoffee' }}</strong>
    </div>
    <div class="ig-media" :style="{ aspectRatio: ratio }">
      <template v-if="current">
        <video v-if="current.kind === 'video'" :src="current.url" muted loop autoplay playsinline />
        <img v-else :src="current.url" alt="" />
      </template>
      <div v-else class="ig-empty">加入圖片或影片後顯示預覽</div>
      <template v-if="media.length > 1">
        <button v-if="idx > 0" class="nav prev" aria-label="上一張" @click="idx--">‹</button>
        <button v-if="idx < media.length - 1" class="nav next" aria-label="下一張" @click="idx++">›</button>
        <div class="dots">
          <i v-for="(_, i) in media" :key="i" :class="{ on: i === idx }" />
        </div>
      </template>
      <div v-if="kind === 'story'" class="story-bar">
        <img :src="avatarSrc" alt="" class="ig-avatar" @error="avatarFailed = true" />
        <strong>{{ username || 'littycoffee' }}</strong>
      </div>
    </div>
    <div v-if="kind !== 'story'" class="ig-body">
      <div class="ig-actions">♡ &nbsp;💬 &nbsp;➤</div>
      <p class="ig-caption" :class="{ clamp: !expanded }" @click="expanded = !expanded">
        <strong>{{ username || 'littycoffee' }}</strong> {{ caption || '（文案）' }}
      </p>
    </div>
  </div>
</template>

<style scoped lang="scss">
.ig {
  width: 100%;
  max-width: 340px;
  margin: 0 auto;
  background: #fff;
  border: 1px solid #dbdbdb;
  border-radius: 12px;
  overflow: hidden;
  font-family: -apple-system, BlinkMacSystemFont, 'PingFang TC', sans-serif;
  font-size: 13px;
  color: #262626;
  &.story,
  &.reel {
    max-width: 260px;
  }
}
.ig-head,
.story-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
}
.ig-avatar {
  width: 28px;
  height: 28px;
  border-radius: 50%;
}
.ig-media {
  position: relative;
  background: #efefef;
  > img,
  > video {
    width: 100%;
    height: 100%;
    object-fit: cover;
    display: block;
  }
}
.ig-empty {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  color: #8e8e8e;
  font-size: 12px;
  padding: 16px;
  text-align: center;
}
.nav {
  position: absolute;
  top: 50%;
  transform: translateY(-50%);
  width: 26px;
  height: 26px;
  border: 0;
  border-radius: 50%;
  background: rgba(255, 255, 255, 0.85);
  font-size: 18px;
  line-height: 1;
  cursor: pointer;
  &.prev {
    left: 8px;
  }
  &.next {
    right: 8px;
  }
}
.dots {
  position: absolute;
  bottom: 8px;
  left: 0;
  right: 0;
  display: flex;
  justify-content: center;
  gap: 4px;
  i {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: rgba(255, 255, 255, 0.5);
    &.on {
      background: #fff;
    }
  }
}
.story-bar {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  color: #fff;
  background: linear-gradient(rgba(0, 0, 0, 0.35), transparent);
}
.ig-body {
  padding: 8px 12px 12px;
}
.ig-actions {
  font-size: 16px;
  margin-bottom: 6px;
}
.ig-caption {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.45;
  cursor: pointer;
  &.clamp {
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
}
</style>
