<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Iphone, Key, SwitchButton } from '@element-plus/icons-vue'
import { useAuthStore } from '@/stores/auth'
import { useInboxStore } from '@/stores/inbox'
import { useThemeStore } from '@/stores/theme'
import type { NavItem } from '@/router/nav'
import PasswordDialog from './PasswordDialog.vue'
import TotpDialog from './TotpDialog.vue'

// 頭像選單：姓名／角色、深色模式、修改密碼、登出。side＝桌機側欄底部、top＝手機頂欄
const props = defineProps<{ variant: 'side' | 'top'; links?: NavItem[] }>()

const router = useRouter()
const auth = useAuthStore()
const inbox = useInboxStore()
const theme = useThemeStore()

const open = ref(false)
const pwDialog = ref(false)
const totpDialog = ref(false)

const name = computed(() => auth.user?.displayName || auth.user?.username || '')
const initial = computed(() => (name.value || '?').slice(0, 1).toUpperCase())
const role = computed(() => (auth.user?.isSuper ? '超管' : auth.user?.isOwner ? '店長' : '管理員'))
// 一般管理員可自選綁定 GA（超管在首次登入時強制綁；已綁定的要由超管重設）
const canBindTotp = computed(() => !!auth.user && !auth.user.isSuper && !auth.user.totpEnabled)

function go(n: NavItem) {
  open.value = false
  router.push({ name: n.name })
}

function openPassword() {
  open.value = false
  pwDialog.value = true
}

function openTotp() {
  open.value = false
  totpDialog.value = true
}

async function onLogout() {
  open.value = false
  inbox.disconnect()
  await auth.logout()
  router.replace({ name: 'login' })
}
</script>

<template>
  <el-popover
    v-model:visible="open"
    trigger="click"
    :placement="variant === 'side' ? 'top-start' : 'bottom-end'"
    :width="240"
    :show-arrow="false"
    popper-class="user-menu-pop"
  >
    <template #reference>
      <button v-if="variant === 'side'" type="button" class="trigger side" aria-label="帳號選單" :aria-expanded="open">
        <span class="avatar">{{ initial }}</span>
        <span class="who">{{ name }}</span>
        <span class="role-chip">{{ role }}</span>
      </button>
      <button v-else type="button" class="trigger top" aria-label="帳號選單" :aria-expanded="open">
        <span class="avatar">{{ initial }}</span>
      </button>
    </template>

    <div class="menu">
      <div class="profile">
        <span class="avatar big">{{ initial }}</span>
        <div class="names">
          <div class="name">{{ name }}</div>
          <div class="role">{{ role }}</div>
        </div>
      </div>
      <template v-if="props.links?.length">
        <button v-for="n in props.links" :key="n.name" type="button" class="item" @click="go(n)">
          <el-icon><component :is="n.icon" /></el-icon>{{ n.title }}
        </button>
        <div class="sep" />
      </template>
      <div class="row">
        <label for="lc-dark-switch">深色模式</label>
        <el-switch
          id="lc-dark-switch"
          :model-value="theme.isDark"
          aria-label="深色模式"
          @update:model-value="(v: string | number | boolean) => theme.setMode(v ? 'dark' : 'light')"
        />
      </div>
      <button type="button" class="item" @click="openPassword">
        <el-icon><Key /></el-icon>修改密碼
      </button>
      <button v-if="canBindTotp" type="button" class="item" @click="openTotp">
        <el-icon><Iphone /></el-icon>綁定 Google Authenticator
      </button>
      <button type="button" class="item danger" @click="onLogout">
        <el-icon><SwitchButton /></el-icon>登出
      </button>
    </div>
  </el-popover>

  <PasswordDialog v-model="pwDialog" />
  <TotpDialog v-model="totpDialog" />
</template>

<style scoped lang="scss">
.trigger {
  border: 0;
  background: transparent;
  font: inherit;
  color: inherit;
  cursor: pointer;
  padding: 0;
}
.trigger.side {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 8px;
  border-radius: 12px;
  text-align: left;
  transition: background 0.15s;
  &:hover,
  &[aria-expanded='true'] {
    background: var(--lc-side-hover);
  }
  .who {
    flex: 1;
    min-width: 0;
    font-size: 14px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
}
.role-chip {
  flex: none;
  font-size: 11px;
  padding: 1px 8px;
  border-radius: 999px;
  background: var(--lc-side-line);
  color: var(--lc-latte);
}
.avatar {
  width: 34px;
  height: 34px;
  border-radius: 50%;
  display: grid;
  place-items: center;
  background: var(--lc-caramel);
  color: var(--lc-on-fill);
  font-weight: 700;
  font-size: 14px;
  flex-shrink: 0;
  &.big {
    width: 40px;
    height: 40px;
    font-size: 16px;
  }
}
.menu {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin: -6px;
}
.profile {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 10px 12px;
  border-bottom: 1px solid var(--lc-border);
  margin-bottom: 4px;
  .names {
    min-width: 0;
  }
  .name {
    font-weight: 600;
    font-size: 14px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .role {
    font-size: 12.5px;
    color: var(--lc-muted);
  }
}
.sep {
  height: 1px;
  margin: 4px 0;
  background: var(--lc-border);
}
.row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 10px;
  font-size: 14px;
  color: var(--lc-text);
}
.item {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  border: 0;
  background: transparent;
  font: inherit;
  font-size: 14px;
  color: var(--lc-text);
  padding: 9px 10px;
  border-radius: 8px;
  cursor: pointer;
  text-align: left;
  &:hover {
    background: var(--lc-bg-hover);
  }
  &.danger {
    color: var(--lc-fail-fg);
  }
}
@media (max-width: 768px) {
  // 手機頂欄頭像：視覺 34px，點擊範圍 40px
  .trigger.top {
    display: grid;
    place-items: center;
    min-width: 40px;
    min-height: 40px;
    margin-right: -3px;
  }
}
</style>
