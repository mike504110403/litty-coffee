<script setup lang="ts">
import HelpTip from '@/components/HelpTip.vue'
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { automationApi, type AutoSettings } from '@/api/automation'
import { useIsMobile } from '@/composables/useIsMobile'

const props = defineProps<{ aiConfigured: boolean; accountId: number }>()

const isMobile = useIsMobile() // 手機時間欄位不可打字，避免彈出鍵盤
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
      title="後端尚未設定 ANTHROPIC_API_KEY，目前只會跑關鍵字規則"
      class="alert"
    />
    <el-form v-if="form" label-position="top" @submit.prevent="save">
      <el-form-item>
        <div class="switch-row">
          <div class="label">
            自動回覆總開關
            <HelpTip text="關閉後這個帳號的私訊只收不回：不跑關鍵字規則與 AI，也不發轉真人通知。總覽頁也能切換。" />
          </div>
          <el-switch v-model="form.autoReplyEnabled" />
        </div>
      </el-form-item>
      <el-form-item>
        <div class="switch-row">
          <div class="label">AI 自動回覆<HelpTip text="關鍵字規則沒命中時，由 AI 依知識庫回答。關閉後只跑規則。" /></div>
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
      <el-form-item>
        <template #label>轉真人時送出的訊息<HelpTip text="AI 沒把握、或命中「轉真人」規則時送出；留空則不送訊息，直接轉真人。" /></template>
        <el-input v-model="form.handoffMessage" type="textarea" :autosize="{ minRows: 2, maxRows: 4 }" maxlength="300" />
      </el-form-item>
      <el-form-item>
        <template #label>合併等待（秒）<HelpTip text="客人常一次連傳好幾則；等這段時間沒有新訊息才回，避免一則一則各回一次。" /></template>
        <el-slider v-model="form.debounceSecs" :min="0" :max="15" show-input class="slider" />
      </el-form-item>

      <h3 class="section">騷擾與大量訊息</h3>
      <el-form-item>
        <template #label>
          大量訊息門檻
          <HelpTip text="超過門檻後降低回覆頻率（不會封鎖）：自動標成問題顧客，之後每 5、10、20…分鐘最多自動回一次，AI 會回得簡短並請對方稍候店員；顧客安靜一段時間後恢復。填 0＝關閉。" />
        </template>
        <div class="spam-row">
          <el-input-number v-model="spamMinutes" :min="1" :max="1440" :step="1" controls-position="right" class="num" />
          <span>分鐘內超過</span>
          <el-input-number v-model="form.spamMaxMsgs" :min="0" :max="500" :step="1" controls-position="right" class="num" />
          <span>則</span>
        </div>
      </el-form-item>
      <el-form-item>
        <div class="switch-row">
          <div class="label">
            AI 判斷騷擾
            <HelpTip text="AI 回覆時順便判斷是否為騷擾、辱罵、性內容、詐騙或打聽他人個資，是就自動列入黑名單、不回覆。一般客訴不算，仍會轉真人。" />
          </div>
          <el-switch v-model="form.aiModeration" />
        </div>
      </el-form-item>
      <h3 class="section">每日摘要</h3>
      <el-form-item>
        <template #label>
          產生時間
          <HelpTip text="每天這個時間（店家時區）整理前一天的私訊：總量、熱門問題與待處理對話，顯示在總覽頁。" />
        </template>
        <el-time-select
          v-model="form.digestTime"
          start="00:00"
          end="23:30"
          step="00:30"
          :clearable="false"
          :editable="!isMobile"
          class="digest-time"
        />
      </el-form-item>
      <h3 class="section">蒸餾建議</h3>
      <el-form-item>
        <div class="switch-row">
          <div class="label">
            每週自動蒸餾
            <HelpTip text="每週一 09:00（店家時區）把近 7 天的對話整理成規則與知識庫建議，完成時通知；建議要在「蒸餾建議」分頁採用後才生效。" />
          </div>
          <el-switch v-model="form.distillWeekly" :disabled="!aiConfigured && !form.distillWeekly" />
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
.digest-time {
  width: 140px;
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
