<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import { adminsApi, permMeta } from '@/api/admins'
import { useAuthStore, type AdminUser, type Perm } from '@/stores/auth'
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
    allPerms.value = data.permissions
  } finally {
    loading.value = false
  }
}
onMounted(load)

const isSelf = (u: AdminUser) => u.id === me.value?.id
// 非店長不能動店長帳號（後端同樣會擋）
const canManage = (u: AdminUser) => !!me.value?.isOwner || !u.isOwner

function roleSummary(u: AdminUser): string {
  if (u.isOwner) return '全部權限'
  if (!u.permissions.length) return '沒有任何功能權限'
  return u.permissions.map((p) => permMeta[p]?.label ?? p).join('、')
}

// ---- 新增／編輯 ----
const dialog = ref(false)
const editing = ref<AdminUser | null>(null)
const saving = ref(false)
const formRef = ref<FormInstance>()
const form = reactive({ username: '', displayName: '', password: '', isOwner: false, permissions: [] as Perm[] })

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
  Object.assign(form, { username: '', displayName: '', password: '', isOwner: false, permissions: [] })
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
    permissions: u.isOwner ? [] : [...u.permissions],
  })
  dialog.value = true
  formRef.value?.clearValidate()
}

// 非店長只能授予自己有的權限；對方原本就有的可以保留或拿掉
function permDisabled(p: Perm): boolean {
  if (form.isOwner) return true
  if (editing.value && isSelf(editing.value) && p === 'admins') return true // 不能拿掉自己的管理員權限
  if (me.value?.isOwner || auth.can(p)) return false
  return !(editing.value?.permissions.includes(p) && !editing.value.isOwner)
}

const ownerSwitchDisabled = computed(() => !me.value?.isOwner)

// 店長把自己降級時，自動保留自己的「管理員」權限（後端也不允許拿掉）
watch(
  () => form.isOwner,
  (owner) => {
    if (!owner && editing.value && isSelf(editing.value) && !form.permissions.includes('admins')) {
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
        permissions: form.isOwner ? undefined : form.permissions,
      })
      if (isSelf(u)) await auth.fetchMe()
      ElMessage.success('已更新')
    } else {
      await adminsApi.create({ ...form, permissions: form.isOwner ? [] : form.permissions })
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
      <p class="muted">每位管理員用自己的帳號登入；店長擁有全部權限，其他人依勾選的功能使用後台。</p>
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
          <el-tag v-if="u.isOwner" size="small" type="warning" effect="light">店長</el-tag>
          <span class="perms">{{ roleSummary(u) }}</span>
        </div>
        <div class="login">
          <span class="label">最後登入</span>
          {{ u.lastLoginAt ? shortTime(u.lastLoginAt) : '從未登入' }}
        </div>
        <div class="status">
          <el-tag size="small" :type="u.disabled ? 'info' : 'success'" effect="light">
            {{ u.disabled ? '已停用' : '啟用中' }}
          </el-tag>
        </div>
        <div v-if="canManage(u)" class="ops">
          <el-button size="small" text @click="openEdit(u)">編輯</el-button>
          <el-button v-if="!isSelf(u)" size="small" text @click="resetPassword(u)">重設密碼</el-button>
          <el-button v-if="!isSelf(u)" size="small" text :type="u.disabled ? 'primary' : 'danger'" @click="toggleDisabled(u)">
            {{ u.disabled ? '啟用' : '停用' }}
          </el-button>
        </div>
        <div v-else class="ops muted small">只有店長能修改</div>
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
        <el-form-item label="店長">
          <div class="owner-row">
            <el-switch v-model="form.isOwner" :disabled="ownerSwitchDisabled" />
            <span class="muted small">
              {{ ownerSwitchDisabled ? '只有店長能設定店長' : '店長擁有全部權限，且不受下方勾選限制' }}
            </span>
          </div>
        </el-form-item>
        <el-form-item label="功能權限">
          <el-checkbox-group v-if="!form.isOwner" v-model="form.permissions" class="perm-grid">
            <el-checkbox v-for="p in allPerms" :key="p" :value="p" :disabled="permDisabled(p)">
              <span class="perm-label">{{ permMeta[p]?.label ?? p }}</span>
              <span class="perm-desc">{{ permMeta[p]?.desc }}</span>
            </el-checkbox>
          </el-checkbox-group>
          <p v-else class="muted small">店長擁有全部權限。</p>
          <p class="muted small hint">總覽與設定頁的「外觀」「管理員密碼」所有人都看得到。</p>
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
.head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
  p {
    margin: 0;
    line-height: 1.6;
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
  grid-template-columns: minmax(140px, 1.2fr) minmax(140px, 1.6fr) 110px 70px auto;
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
    flex-direction: column;
    white-space: normal;
    line-height: 1.4;
  }
}
.perm-label {
  font-size: 14px;
}
.perm-desc {
  font-size: 12px;
  color: var(--lc-muted);
}
.hint {
  margin: 8px 0 0;
}
@media (max-width: 768px) {
  .head {
    flex-direction: column;
  }
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
