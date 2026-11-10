<script setup lang="ts">
import { reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { devApi } from '@/api/inbox'

const visible = defineModel<boolean>({ required: true })

const form = reactive({ username: 'latte_lover', name: '小拿', text: '' })
const presets = ['請問今天有營業嗎？', '菜單在哪裡可以看？', '想訂位，週六下午 4 個人', '有提供插座跟 wifi 嗎？', '可以帶狗狗嗎']
const people = [
  { username: 'latte_lover', name: '小拿' },
  { username: 'mocha.chen', name: '陳摩卡' },
  { username: 'oatmilk_fan', name: '燕麥奶控' },
]
const sending = ref(false)

async function send() {
  if (!form.username.trim() || !form.text.trim()) return
  sending.value = true
  try {
    await devApi.simulate({ ...form })
    ElMessage.success('已送出模擬訊息')
    form.text = ''
  } finally {
    sending.value = false
  }
}
</script>

<template>
  <el-dialog v-model="visible" title="模擬客人傳 DM" width="min(440px, 92vw)" align-center>
    <p class="note">開發模式專用：組出和 IG webhook 相同格式的事件，走完整的入庫流程。</p>
    <el-form label-position="top" @submit.prevent="send">
      <el-form-item label="客人">
        <div class="people">
          <el-check-tag
            v-for="p in people"
            :key="p.username"
            :checked="form.username === p.username"
            @change="Object.assign(form, p)"
          >
            {{ p.name }}
          </el-check-tag>
        </div>
        <div class="who-inputs">
          <el-input v-model="form.username" placeholder="IG 帳號"><template #prepend>@</template></el-input>
          <el-input v-model="form.name" placeholder="名稱" />
        </div>
      </el-form-item>
      <el-form-item label="訊息">
        <el-input v-model="form.text" type="textarea" :rows="3" placeholder="輸入客人的訊息" />
        <div class="presets">
          <el-tag v-for="p in presets" :key="p" class="preset" effect="plain" round @click="form.text = p">{{ p }}</el-tag>
        </div>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="visible = false">關閉</el-button>
      <el-button type="primary" :loading="sending" :disabled="!form.text.trim()" @click="send">送出</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.note {
  margin: -6px 0 14px;
  font-size: 13px;
  color: var(--lc-muted);
}
.people {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}
.who-inputs {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
  width: 100%;
}
.presets {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 8px;
}
.preset {
  cursor: pointer;
}
</style>
