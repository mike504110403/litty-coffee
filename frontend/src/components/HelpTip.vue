<script setup lang="ts">
import { computed } from 'vue'
import { QuestionFilled } from '@element-plus/icons-vue'

// 「?」說明：桌機 hover／focus 顯示 tooltip，觸控裝置（沒有 hover）點一下開 popover。
// 文字用 text，或放進預設 slot（可帶簡單排版）。
const props = withDefaults(defineProps<{ text?: string; placement?: 'top' | 'bottom' | 'left' | 'right' | 'bottom-end'; width?: number }>(), {
  placement: 'top',
  width: 280,
})

const canHover = computed(() => typeof window !== 'undefined' && window.matchMedia('(hover: hover)').matches)
const trigger = computed(() => (canHover.value ? 'hover' : 'click'))
</script>

<template>
  <el-popover :trigger="trigger" :placement="props.placement" :width="props.width" popper-class="help-tip-pop" :show-arrow="false">
    <template #reference>
      <button type="button" class="help-tip" aria-label="說明" @click.prevent>
        <el-icon :size="15"><QuestionFilled /></el-icon>
      </button>
    </template>
    <div class="help-body"><slot>{{ props.text }}</slot></div>
  </el-popover>
</template>

<style scoped lang="scss">
.help-tip {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  padding: 0;
  margin-left: 4px;
  border: 0;
  border-radius: 50%;
  background: transparent;
  color: var(--lc-muted);
  vertical-align: middle;
  cursor: help;
  flex: none;
  position: relative;
  @media (max-width: 768px) {
    &::after {
      content: '';
      position: absolute;
      inset: -10px; // 手機點擊範圍放大到 40px，不影響版面
    }
  }
  &:hover,
  &:focus-visible {
    color: var(--el-color-primary);
  }
}
.help-body {
  font-size: 13px;
  line-height: 1.6;
  color: var(--lc-text);
  font-weight: 400;
  white-space: normal;
  text-align: left;
}
</style>
