<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { accountLabel } from '@/api/accounts'
import { useAccountsStore } from '@/stores/accounts'
import AccountAvatar from './AccountAvatar.vue'

// 帳號選擇器。allowAll＝篩選用（null 代表「全部帳號」）；否則必選一個帳號
const model = defineModel<number | null>({ required: true })
const props = withDefaults(
  defineProps<{ allowAll?: boolean; size?: 'small' | 'default' | 'large'; placeholder?: string }>(),
  { size: 'default', placeholder: '選擇 IG 帳號' },
)
const accounts = useAccountsStore()
// el-option 不收 null：「全部帳號」內部用 0 代表
const ALL = 0
const inner = computed<number | undefined>({
  get: () => (model.value == null ? (props.allowAll ? ALL : undefined) : model.value),
  set: (v) => (model.value = v == null || v === ALL ? null : v),
})
onMounted(() => void accounts.ensure())
</script>

<template>
  <el-select v-model="inner" class="account-select" :size="size" :placeholder="placeholder">
    <template #prefix>
      <AccountAvatar v-if="model != null" :account="accounts.byId.get(model)" :size="18" />
    </template>
    <el-option v-if="allowAll" :value="ALL" label="全部帳號" />
    <el-option v-for="a in accounts.list" :key="a.id" :value="a.id" :label="accountLabel(a)">
      <span class="opt">
        <AccountAvatar :account="a" :size="18" />
        <span>{{ accountLabel(a) }}</span>
        <span v-if="!a.active" class="off">已停用</span>
        <span v-else-if="!a.connected" class="off">需重新連結</span>
      </span>
    </el-option>
  </el-select>
</template>

<style scoped>
.account-select {
  width: 200px;
}
.opt {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.off {
  font-size: 12px;
  color: var(--lc-faint);
}
</style>
