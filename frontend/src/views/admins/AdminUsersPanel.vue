<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import { adminsApi, permMeta } from '@/api/admins'
import { useAuthStore, type AdminUser, type Perm } from '@/stores/auth'
import HelpTip from '@/components/HelpTip.vue'
import { shortTime } from '@/utils/time'

const auth = useAuthStore()
const me = computed(() => auth.user)

const admins = ref<AdminUser[]>([])
const allPerms = ref<Perm[]>([])
const loading = ref(true)

async function load() {
  loading.value = true
  try {
    const data = await adminsApi.list()
    admins.value = data.admins
    // ig_accounts、credentials 是超管專屬，不可勾選（後端清單已不含，這裡再保險過濾）
    allPerms.value = data.permissions.filter((p) => p !== 'ig_accounts' && p !== 'credentials')
  } finally {
    loading.value = false
  }
}
onMounted(load)

const isSelf = (u: AdminUser) => u.id === me.value?.id
const meSuper = computed(() => !!me.value?.isSuper)
const meOwnerLevel = computed(() => !!me.value?.isOwner || meSuper.value)
// 超管帳號只有超管能動；店長帳號只有店長、超管能動（後端同樣會擋）
const canManage = (u: AdminUser) => (u.isSuper ? meSuper.value : meOwnerLevel.value || !u.isOwner)

function roleSummary(u: AdminUser): string {
  if (u.isSuper) return '全部權限，含 IG 帳號與憑證'
  if (u.isOwner) return '全部權限'
  if (!u.permissions.length) return '沒有任何功能權限'
  return u.permissions.map((p) => permMeta[p]?.label ?? p).join('、')
}

// ---- 新增／編輯 ----
const dialog = ref(false)
const editing = ref<AdminUser | null>(null)
const saving = ref(false)
const formRef = ref<FormInstance>()
const form = reactive({ username: '', displayName: '', password: '', isOwner: false, isSuper: false, permissions: [] as Perm[] })

const rules: FormRules = {
  username: [
    { required: true, message: '請輸入登入帳號', trigger: 'blur' },
    { pattern: /^\S+$/, message: '帳號不能有空白', trigger: 'blur' },
    { max: 50, message: '最多 50 個字', trigger: 'blur' },
  ],
  password: [
    { required: true, message: '請輸入初始密碼', trigger: 'blur' },
    { min: 8, message: '至少 8 個字元', trigger: 'blur' },
  ],
}

function openCreate() {
  editing.value = null
  Object.assign(form, { username: '', displayName: '', password: '', isOwner: false, isSuper: false, permissions: [] })
  dialog.value = true
  formRef.value?.clearValidate()
}

function openEdit(u: AdminUser) {
  editing.value = u
  Object.assign(form, {
    username: u.username,
    displayName: u.displayName,
    password: '',
    isOwner: u.isOwner,
    isSuper: u.isSuper,
    permissions: u.isOwner || u.isSuper ? [] : [...u.permissions],
  })
  dialog.value = true
  formRef.value?.clearValidate()
}

// 非店長只能授予自己有的權限；對方原本就有的可以保留或拿掉
function permDisabled(p: Perm): boolean {
  if (allPermsGranted.value) return true
  if (editing.value && isSelf(editing.value) && p === 'admins') return true // 不能拿掉自己的管理員權限
  if (meOwnerLevel.value || auth.can(p)) return false
  return !(editing.value?.permissions.includes(p) && !editing.value.isOwner)
}

const allPermsGranted = computed(() => form.isOwner || form.isSuper)
const ownerSwitchDisabled = computed(() => !meOwnerLevel.value)

// 店長／超管把自己降級時，自動保留自己的「管理員」權限（後端也不允許拿掉）
watch(
  allPermsGranted,
  (all) => {
    if (!all && editing.value && isSelf(editing.value) && !form.permissions.includes('admins')) {
      form.permissions.push('admins')
    }
  },
)

async function save() {
  if (!(await formRef.value?.validate().catch(() => false))) return
  saving.value = true
  try {
    if (editing.value) {
      const u = editing.value
      await adminsApi.update(u.id, {
        displayName: form.displayName,
        isOwner: form.isOwner,
        isSuper: meSuper.value ? form.isSuper : undefined,
        permissions: allPermsGranted.value ? undefined : form.permissions,
      })
      if (isSelf(u)) await auth.fetchMe()
      ElMessage.success('已更新')
    } else {
      await adminsApi.create({
        ...form,
        isSuper: meSuper.value ? form.isSuper : undefined,
        permissions: allPermsGranted.value ? [] : form.permissions,
      })
      ElMessage.success('已新增管理員')
    }
    dialog.value = false
    await load()
  } catch {
    // 錯誤訊息已由 http 攔截器顯示
  } finally {
    saving.value = false
  }
}

// ---- 重設密碼 ----
async function resetPassword(u: AdminUser) {
  let value: string
  try {
    ;({ value } = await ElMessageBox.prompt(`為「${u.displayName || u.username}」設定新密碼`, '重設密碼', {
      confirmButtonText: '重設',
      cancelButtonText: '取消',
      inputType: 'password',
      inputPlaceholder: '至少 8 個字元',
      inputValidator: (v: string) => (v && v.length >= 8) || '至少 8 個字元',
    }))
  } catch {
    return
  }
  try {
    await adminsApi.resetPassword(u.id, value)
    ElMessage.success('密碼已重設，請轉告對方')
  } catch {
    // 攔截器已顯示
  }
}

// ---- 重設 GA（超管專屬）----
async function resetTotp(u: AdminUser) {
  try {
    await ElMessageBox.confirm(
      u.isSuper ? '對方會立刻被登出，重新登入後必須重新綁定。' : '對方下次登入不必輸入驗證碼，可再自行綁定。原本的備援碼一併作廢。',
      `重設「${u.displayName || u.username}」的 Google Authenticator？`,
      { confirmButtonText: '重設', cancelButtonText: '取消', type: 'warning', confirmButtonClass: 'el-button--danger' },
    )
  } catch {
    return
  }
  try {
    await adminsApi.resetTotp(u.id)
    ElMessage.success('已重設 GA')
    await load()
  } catch {
    // 攔截器已顯示
  }
}

// ---- 停用／啟用 ----
async function toggleDisabled(u: AdminUser) {
  const next = !u.disabled
  if (next) {
    try {
      await ElMessageBox.confirm('停用後對方會立刻被登出，也無法再登入。之後可以再啟用。', `停用「${u.displayName || u.username}」？`, {
        confirmButtonText: '停用',
        cancelButtonText: '取消',
        type: 'warning',
        confirmButtonClass: 'el-button--danger',
      })
    } catch {
      return
    }
  }
  try {
    await adminsApi.update(u.id, { disabled: next })
    ElMessage.success(next ? '已停用' : '已啟用')
    await load()
  } catch {
    // 攔截器已顯示
  }
}
</script>

<template>
  <div v-loading="loading" class="admins">
    <div class="head">
      <div class="head-title">
        共 {{ admins.length }} 位
        <HelpTip text="每位管理員用自己的帳號登入；超管位階最高，只有超管能連結 IG 帳號、設定憑證、重設 GA；店長擁有其他全部權限；其他人依勾選的功能使用後台。總覽、深色模式、修改密碼與綁定 GA（頭像選單）所有人都能用。" />
      </div>
      <el-button type="primary" :icon="Plus" @click="openCreate">新增管理員</el-button>
    </div>

    <ul class="list">
      <li v-for="u in admins" :key="u.id" class="row" :class="{ off: u.disabled }">
        <div class="who">
          <div class="avatar">{{ (u.displayName || u.username).slice(0, 1).toUpperCase() }}</div>
          <div class="names">
            <div class="name">
              {{ u.displayName || u.username }}
              <el-tag v-if="isSelf(u)" size="small" effect="plain">你</el-tag>
            </div>
            <div class="account">@{{ u.username }}</div>
          </div>
        </div>
        <div class="role">
          <el-tag v-if="u.isSuper" size="small" type="danger" effect="light">超管</el-tag>
          <el-tag v-if="u.isOwner" size="small" type="warning" effect="light">店長</el-tag>
          <el-tag v-if="u.totpEnabled" size="small" type="success" effect="plain">GA 已綁定</el-tag>
          <el-tag v-else-if="u.isSuper" size="small" type="info" effect="plain">GA 未綁定</el-tag>
          <span class="perms">{{ roleSummary(u) }}</span>
        </div>
        <div class="login">
          <span class="presence" :class="{ on: u.online }" :aria-label="u.online ? '在線' : '離線'" />
          <template v-if="u.online">在線</template>
          <template v-else>
            <span class="label">最後活動</span>
            {{ u.lastSeenAt || u.lastLoginAt ? shortTime((u.lastSeenAt || u.lastLoginAt)!) : '從未登入' }}
          </template>
        </div>
        <div class="status">
          <el-tag size="small" :type="u.disabled ? 'info' : 'success'" effect="light">
            {{ u.disabled ? '已停用' : '啟用中' }}
          </el-tag>
        </div>
        <div v-if="canManage(u)" class="ops">
          <el-button size="small" text @click="openEdit(u)">編輯</el-button>
          <el-button v-if="!isSelf(u)" size="small" text @click="resetPassword(u)">重設密碼</el-button>
          <el-button v-if="meSuper && !isSelf(u) && u.totpEnabled" size="small" text @click="resetTotp(u)">重設 GA</el-button>
          <el-button v-if="!isSelf(u)" size="small" text :type="u.disabled ? 'primary' : 'danger'" @click="toggleDisabled(u)">
            {{ u.disabled ? '啟用' : '停用' }}
          </el-button>
        </div>
        <div v-else class="ops muted small">{{ u.isSuper ? '只有超管能修改' : '只有店長能修改' }}</div>
      </li>
    </ul>

    <el-dialog v-model="dialog" :title="editing ? '編輯管理員' : '新增管理員'" width="min(520px, 92vw)" append-to-body>
      <el-form ref="formRef" :model="form" :rules="editing ? {} : rules" label-position="top" @submit.prevent="save">
        <el-form-item label="登入帳號" prop="username">
          <el-input v-model="form.username" :disabled="!!editing" autocomplete="off" placeholder="例如 mei" />
        </el-form-item>
        <el-form-item label="顯示名稱">
          <el-input v-model="form.displayName" placeholder="回覆顧客時顯示在訊息下方，例如 小美" />
        </el-form-item>
        <el-form-item v-if="!editing" label="初始密碼" prop="password">
          <el-input v-model="form.password" type="password" show-password autocomplete="new-password" />
        </el-form-item>
        <el-form-item v-if="meSuper" label="超級管理員">
          <div class="owner-row">
            <el-switch v-model="form.isSuper" />
            <HelpTip text="超管擁有全部權限，並且是唯一能連結 IG 帳號、設定憑證、重設 GA 的角色。第一次登入必須綁定 Google Authenticator。" />
          </div>
        </el-form-item>
        <el-form-item label="店長">
          <div class="owner-row">
            <el-switch v-model="form.isOwner" :disabled="ownerSwitchDisabled" />
            <HelpTip :text="ownerSwitchDisabled ? '只有店長能設定店長。' : '店長擁有 IG 帳號與憑證以外的全部權限，且不受下方勾選限制。'" />
          </div>
        </el-form-item>
        <el-form-item label="功能權限">
          <el-checkbox-group v-if="!allPermsGranted" v-model="form.permissions" class="perm-grid">
            <el-checkbox v-for="p in allPerms" :key="p" :value="p" :disabled="permDisabled(p)">
              <span class="perm-label">{{ permMeta[p]?.label ?? p }}</span>
              <HelpTip v-if="permMeta[p]?.desc" :text="permMeta[p]?.desc" />
            </el-checkbox>
          </el-checkbox-group>
          <p v-else class="muted small">{{ form.isSuper ? '超管擁有全部權限。' : '店長擁有全部權限。' }}</p>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">{{ editing ? '儲存' : '新增' }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped lang="scss">
.presence {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 6px;
  vertical-align: middle;
  background: var(--lc-border);
  &.on {
    background: var(--el-color-success);
  }
}
.head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
  .head-title {
    display: flex;
    align-items: center;
    font-size: 14px;
    font-weight: 600;
    min-height: 32px;
  }
  .el-button {
    flex: none;
  }
}
.muted {
  color: var(--lc-muted);
  font-size: 13px;
}
.small {
  font-size: 12.5px;
}
.list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.row {
  display: grid;
  grid-template-columns: minmax(150px, 1.1fr) minmax(160px, 1.8fr) 100px 76px 200px;
  align-items: center;
  gap: 12px;
  padding: 12px 0;
  border-top: 1px solid var(--lc-border);
  &:first-child {
    border-top: 0;
  }
  &.off .who,
  &.off .role {
    opacity: 0.55;
  }
}
.who {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}
.avatar {
  width: 34px;
  height: 34px;
  flex: none;
  border-radius: 50%;
  display: grid;
  place-items: center;
  background: var(--lc-caramel);
  color: var(--lc-on-fill);
  font-weight: 600;
  font-size: 14px;
}
.names {
  min-width: 0;
}
.name {
  font-weight: 600;
  font-size: 14px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.account {
  color: var(--lc-muted);
  font-size: 12.5px;
  overflow: hidden;
  text-overflow: ellipsis;
}
.role {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  font-size: 13px;
  min-width: 0;
}
.perms {
  color: var(--lc-text);
}
.login {
  font-size: 13px;
  .label {
    display: none;
  }
}
.ops {
  display: flex;
  justify-content: flex-end;
  flex-wrap: wrap;
  .el-button + .el-button {
    margin-left: 0;
  }
}
.owner-row {
  display: flex;
  align-items: center;
  gap: 10px;
}
.perm-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 4px 16px;
  width: 100%;
  :deep(.el-checkbox) {
    height: auto;
    margin-right: 0;
    align-items: flex-start;
    padding: 4px 0;
  }
  :deep(.el-checkbox__label) {
    display: flex;
    align-items: center;
    white-space: normal;
    line-height: 1.4;
  }
}
.perm-label {
  font-size: 14px;
}
@media (max-width: 768px) {
  .row {
    grid-template-columns: 1fr auto;
    gap: 6px 12px;
  }
  .who {
    grid-column: 1;
  }
  .status {
    grid-column: 2;
    grid-row: 1;
    justify-self: end;
  }
  .role,
  .login {
    grid-column: 1 / -1;
  }
  .login .label {
    display: inline;
    color: var(--lc-muted);
    margin-right: 6px;
  }
  .ops {
    grid-column: 1 / -1;
    justify-content: flex-start;
  }
  .perm-grid {
    grid-template-columns: 1fr;
  }
}
</style>
