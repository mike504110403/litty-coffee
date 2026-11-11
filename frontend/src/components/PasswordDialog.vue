<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { settingsApi } from '@/api/settings'

const visible = defineModel<boolean>({ required: true })

const formRef = ref<FormInstance>()
const pw = reactive({ current: '', next: '', confirm: '' })
const saving = ref(false)
const rules: FormRules = {
  current: [{ required: true, message: '請輸入目前密碼', trigger: 'blur' }],
  next: [
    { required: true, message: '請輸入新密碼', trigger: 'blur' },
    { min: 8, message: '至少 8 個字元', trigger: 'blur' },
  ],
  confirm: [
    { required: true, message: '請再輸入一次新密碼', trigger: 'blur' },
    {
      validator: (_r, v: string, cb) => (v === pw.next ? cb() : cb(new Error('兩次輸入不一致'))),
      trigger: 'blur',
    },
  ],
}

// 每次打開都是乾淨的表單
watch(visible, (v) => {
  if (v) {
    Object.assign(pw, { current: '', next: '', confirm: '' })
    formRef.value?.clearValidate()
  }
})

async function save() {
  if (!(await formRef.value?.validate().catch(() => false))) return
  saving.value = true
  try {
    await settingsApi.changePassword(pw.current, pw.next)
    ElMessage.success('密碼已更新')
    visible.value = false
  } catch {
    // 錯誤訊息已由 http 攔截器顯示
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <el-dialog v-model="visible" title="修改密碼" width="min(400px, 92vw)" append-to-body>
    <el-form ref="formRef" :model="pw" :rules="rules" label-position="top" @submit.prevent="save">
      <el-form-item label="目前密碼" prop="current">
        <el-input v-model="pw.current" type="password" show-password autocomplete="current-password" />
      </el-form-item>
      <el-form-item label="新密碼" prop="next">
        <el-input v-model="pw.next" type="password" show-password autocomplete="new-password" />
      </el-form-item>
      <el-form-item label="再輸入一次" prop="confirm">
        <el-input v-model="pw.confirm" type="password" show-password autocomplete="new-password" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :loading="saving" @click="save">更新密碼</el-button>
    </template>
  </el-dialog>
</template>
