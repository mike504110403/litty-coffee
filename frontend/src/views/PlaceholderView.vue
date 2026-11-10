<script setup lang="ts">
import { useRoute } from 'vue-router'

const route = useRoute()
</script>

<template>
  <div>
    <h1 class="lc-page-title">{{ route.meta.title }}</h1>
    <div class="lc-card empty">
      <el-empty description="這個功能正在烘焙中，下一批就上桌" :image-size="120" />
    </div>
  </div>
</template>

<style scoped>
.empty {
  padding: 40px 16px;
  margin-top: 16px;
}
</style>
