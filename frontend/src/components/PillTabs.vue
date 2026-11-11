<script setup lang="ts" generic="K extends string">
// 膠囊分頁籤（自動回覆、管理員頁、設定頁手機版共用外觀）
defineProps<{ tabs: readonly { key: K; label: string }[]; block?: boolean }>()
const model = defineModel<K>({ required: true })
</script>

<template>
  <div class="pill-tabs" :class="{ block }" role="tablist">
    <button
      v-for="t in tabs"
      :key="t.key"
      type="button"
      role="tab"
      class="pill"
      :class="{ on: model === t.key }"
      :aria-selected="model === t.key"
      @click="model = t.key"
    >
      {{ t.label }}
    </button>
  </div>
</template>

<style scoped lang="scss">
.pill-tabs {
  display: flex;
  gap: 4px;
  padding: 4px;
  border-radius: 14px;
  background: var(--lc-bg-track);
  width: fit-content;
  max-width: 100%;
  overflow-x: auto;
  scrollbar-width: none;
  &.block {
    width: 100%;
    .pill {
      flex: 1;
    }
  }
}
.pill {
  flex-shrink: 0;
  white-space: nowrap;
  border: 0;
  padding: 8px 16px;
  border-radius: 10px;
  background: transparent;
  font: inherit;
  font-size: 14px;
  color: var(--lc-muted);
  cursor: pointer;
  &.on {
    background: var(--lc-bg-tab-on);
    color: var(--lc-text);
    font-weight: 500;
    box-shadow: var(--lc-tab-shadow);
  }
}
@media (max-width: 768px) {
  // 手機：一列可橫滑、不換行；點擊範圍 ≥40px
  .pill-tabs {
    flex-wrap: nowrap;
    -webkit-overflow-scrolling: touch;
    overscroll-behavior-x: contain;
    scroll-snap-type: x proximity;
  }
  .pill {
    min-height: 40px;
    padding: 0 18px;
    scroll-snap-align: center;
  }
}
</style>
