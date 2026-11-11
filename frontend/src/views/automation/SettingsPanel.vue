<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { automationApi, type AutoSettings } from '@/api/automation'

const props = defineProps<{ aiConfigured: boolean; accountId: number }>()

const form = ref<AutoSettings | null>(null)
const saving = ref(false)

onMounted(async () => {
  form.value = await automationApi.settings(props.accountId)
})

// 大量訊息的時間範圍：介面用分鐘、API 用秒
const spamMinutes = computed({
  get: () => Math.max(1, Math.round((form.value?.spamWindowSecs ?? 300) / 60)),
  set: (v: number | undefined) => {
    if (form.value) form.value.spamWindowSecs = Math.max(1, Math.round(v ?? 5)) * 60
  },
})

async function save() {
  if (!form.value) return
  saving.value = true
  try {
    form.value = await automationApi.saveSettings(props.accountId, form.value)
    ElMessage.success('已儲存')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div v-loading="!form" class="wrap lc-card">
    <el-alert
      v-if="!aiConfigured"
      type="warning"
      :closable="false"
      show-icon
      title="後端尚未設定 ANTHROPIC_API_KEY"
      description="目前只會跑關鍵字規則。設定 key 並重啟後端後，AI 才會開始回覆。"
      class="alert"
    />
    <el-form v-if="form" label-position="top" @submit.prevent="save">
      <el-form-item>
        <div class="switch-row">
          <div>
            <div class="label">AI 自動回覆</div>
            <div class="desc">關鍵字規則沒命中時，由 AI 依知識庫回答。關閉後只跑規則。</div>
          </div>
          <el-switch v-model="form.aiEnabled" />
        </div>
      </el-form-item>
      <el-form-item label="AI 的角色與語氣">
        <el-input
          v-model="form.persona"
          type="textarea"
          :autosize="{ minRows: 3, maxRows: 8 }"
          maxlength="1000"
          show-word-limit
        />
      </el-form-item>
      <el-form-item label="轉真人時送出的訊息">
        <el-input v-model="form.handoffMessage" type="textarea" :autosize="{ minRows: 2, maxRows: 4 }" maxlength="300" />
        <div class="help">AI 沒把握、或命中「轉真人」規則時送出；留空則不送訊息，直接轉真人。</div>
      </el-form-item>
      <el-form-item label="合併等待（秒）">
        <el-slider v-model="form.debounceSecs" :min="0" :max="15" show-input class="slider" />
        <div class="help">客人常一次連傳好幾則；等這段時間沒有新訊息才回，避免一則一則各回一次。</div>
      </el-form-item>

      <h3 class="section">騷擾與大量訊息</h3>
      <el-form-item label="大量訊息門檻">
        <div class="spam-row">
          <el-input-number v-model="spamMinutes" :min="1" :max="1440" :step="1" controls-position="right" class="num" />
          <span>分鐘內超過</span>
          <el-input-number v-model="form.spamMaxMsgs" :min="0" :max="500" :step="1" controls-position="right" class="num" />
          <span>則</span>
        </div>
        <div class="help">
          超過門檻後降低回覆頻率（不會封鎖）：自動標成問題顧客，之後每 5、10、20…分鐘最多自動回一次，AI 會回得簡短並請對方稍候店員；顧客安靜一段時間後恢復。填 0 則＝關閉。
        </div>
      </el-form-item>
      <el-form-item>
        <div class="switch-row">
          <div>
            <div class="label">AI 判斷騷擾</div>
            <div class="desc">
              AI 回覆時順便判斷是否為騷擾、辱罵、性內容、詐騙或打聽他人個資，是就自動列入黑名單、不回覆。一般客訴不算，仍會轉真人。
            </div>
          </div>
          <el-switch v-model="form.aiModeration" />
        </div>
      </el-form-item>
      <el-button type="primary" native-type="submit" :loading="saving">儲存設定</el-button>
    </el-form>
  </div>
</template>

<style scoped lang="scss">
.wrap {
  padding: 20px;
  max-width: 720px;
  min-height: 120px;
}
.alert {
  margin-bottom: 18px;
}
.switch-row {
  width: 100%;
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 16px;
  .label {
    font-weight: 500;
  }
  .desc {
    font-size: 13px;
    color: var(--lc-muted);
    line-height: 1.5;
  }
}
.help {
  font-size: 12px;
  color: var(--lc-muted);
  line-height: 1.5;
  margin-top: 6px;
}
.slider {
  width: 100%;
}
.section {
  margin: 28px 0 14px;
  padding-top: 18px;
  border-top: 1px solid var(--lc-border);
  font-size: 15px;
}
.spam-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  .num {
    width: 110px;
  }
}
@media (max-width: 768px) {
  .wrap {
    padding: 16px;
  }
}
</style>
