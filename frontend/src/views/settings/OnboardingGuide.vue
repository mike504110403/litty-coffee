<script setup lang="ts">
// 接上咖啡廳正式 IG 帳號的流程（只給超管看）。內容是靜態說明，與 Meta 介面同步時直接改這個檔。
interface Step {
  title: string
  body: string // 允許 <b>、<code> 等簡單標記（內容寫死在這裡，不含使用者輸入）
  note?: string
}
interface Phase {
  title: string
  who: string
  where: string
  steps: Step[]
}

const owners = [
  { item: 'Meta 開發者帳號', owner: '你', note: '姊姊不用註冊開發者' },
  { item: 'App litty-agent', owner: '你（掛在 Mike dev）', note: '要送審時才轉到咖啡廳的業務組合，你仍是 App 管理員' },
  { item: '業務組合', owner: '姊姊', note: '用咖啡廳登記名建立，你是完整控制管理員' },
  { item: '店的 IG 帳號', owner: '姊姊的業務組合', note: '不放進你的 Mike dev' },
]

const phases: Phase[] = [
  {
    title: '姊姊的手機',
    who: '姊姊',
    where: 'IG App，約 5 分鐘',
    steps: [
      {
        title: '轉成商家帳號',
        body: '<b>個人檔案 → ☰ → 帳號類型和工具 → 切換為專業帳號</b>，類別選「咖啡廳」，類型選<b>商家</b>（不要選創作者）。已經是商家帳號就跳過。',
      },
      {
        title: '允許工具讀取私訊',
        body: '<b>☰ → 訊息和限時動態回覆 → 訊息控制 → 連結的工具 → 允許存取訊息</b>，打開。',
        note: '沒開這個，後台收不到任何私訊，而且不會有錯誤提示。',
      },
    ],
  },
  {
    title: '建姊姊自己的業務組合',
    who: '姊姊和你一起',
    where: '電腦：business.facebook.com/settings',
    steps: [
      {
        title: '姊姊建業務組合，把你加成管理員',
        body: '姊姊用她的 Facebook 登入 → 建立業務組合，名稱用咖啡廳登記名 → <b>帳號 → Instagram 帳號 → 新增</b>，加入店的 IG → <b>用戶 → 人員 → 新增</b>，邀請你、權限選<b>完整控制</b>。你收信接受邀請。',
      },
      {
        title: '把帳號指派給自己',
        body: '同一頁點開店的 IG 帳號 → <b>指派相關人員</b> → 選你自己 → 權限全開。',
      },
      {
        title: '記下帳號編號',
        body: '帳號資訊裡有一串 <code>1784…</code> 開頭的編號，複製起來（@mikedev666 的是 <code>17841467271924092</code>，格式相同）。',
      },
    ],
  },
  {
    title: '後台接上帳號',
    who: '你（超管）',
    where: '這個後台',
    steps: [
      {
        title: '連結新的業務組合',
        body: '<b>設定 → IG 帳號 → 連結 IG 帳號</b> → Meta 視窗選<b>姊姊的業務組合</b>，勾店的 IG，按繼續到完成。會多一張新的連線卡片，舊的 Mike dev 卡片不受影響。',
      },
      {
        title: '帳號沒出現就手動加入',
        body: '新卡片上按<b>手動加入帳號</b>，貼上上一步的編號。加入後要看到「私訊通知已訂閱」。',
      },
      {
        title: '先關自動回覆，填資料',
        body: '<b>自動回覆</b>選店的帳號 → 先關總開關 → 填知識庫（營業時間、地址、菜單與價格、訂位方式）和人設 → 在試答沙盒問幾題，確認回答都對。',
      },
      {
        title: '幫姊姊開後台帳號',
        body: '<b>管理員</b>頁新增帳號、設為<b>店長</b>。她登入後改密碼，建議綁 GA。',
      },
      {
        title: '處理測試帳號',
        body: '@mikedev666 在<b>設定 → IG 帳號</b>按<b>停用</b>（資料保留），或留著當測試帳號。',
      },
    ],
  },
]

const review: Step[] = [
  {
    title: '商家驗證',
    body: '驗證的是<b>擁有 App 的業務組合</b>。先把 App 轉到姊姊的組合（developers.facebook.com → litty-agent → 應用程式設定 → 基本資料 → 業務組合），再到 business.facebook.com → <b>安全中心 → 商家驗證</b>。名稱、地址、電話要和營業登記一致，通常幾天。',
  },
  {
    title: 'App 審查',
    body: 'developers.facebook.com → litty-agent → <b>應用程式審查</b>，申請進階存取：<code>instagram_manage_messages</code>、<code>instagram_basic</code>、<code>instagram_content_publish</code>、<code>instagram_manage_insights</code>、<code>business_management</code>。每項附一段後台實際操作的螢幕錄影。',
  },
]

// 跨階段連續編號
const offsets = phases.reduce<number[]>((acc, _p, i) => {
  acc.push(i === 0 ? 0 : acc[i - 1] + phases[i - 1].steps.length)
  return acc
}, [])
const reviewStart = phases.reduce((n, p) => n + p.steps.length, 0)
</script>

<template>
  <div class="guide">
    <section class="lc-card block">
      <h2>接上咖啡廳的 IG 帳號</h2>
      <p class="lede">
        沿用現在的 App，不用改程式。姊姊只開商家帳號和業務組合，日常只用這個後台，不用開 Meta。
      </p>
      <div class="owners">
        <div v-for="o in owners" :key="o.item" class="owner">
          <span class="o-item">{{ o.item }}</span>
          <span class="o-who">{{ o.owner }}</span>
          <span class="o-note">{{ o.note }}</span>
        </div>
      </div>
    </section>

    <section v-for="(p, pi) in phases" :key="p.title" class="lc-card block">
      <header class="phase-head">
        <h2>{{ p.title }}</h2>
        <span class="who">{{ p.who }}</span>
        <span class="where">{{ p.where }}</span>
      </header>
      <ol class="steps">
        <li v-for="(s, si) in p.steps" :key="s.title">
          <span class="num">{{ offsets[pi] + si + 1 }}</span>
          <div class="s-body">
            <span class="s-title">{{ s.title }}</span>
            <!-- eslint-disable-next-line vue/no-v-html -- 內容寫死在本檔 -->
            <span class="s-text" v-html="s.body" />
            <span v-if="s.note" class="note">{{ s.note }}</span>
          </div>
        </li>
      </ol>
    </section>

    <section class="lc-card block">
      <header class="phase-head">
        <h2>上線前：陌生人私訊測試</h2>
        <span class="who">你</span>
      </header>
      <p class="s-text">找一個和 Meta App 沒有任何關係的朋友帳號，私訊店的 IG，看收件匣有沒有收到。</p>
      <div class="fork">
        <div class="yes"><b>收到</b>可以直接用，打開自動回覆總開關。</div>
        <div class="no"><b>沒收到</b>要先送 Meta 審核，拿到進階存取才收得到一般顧客的訊息。</div>
      </div>
      <ol class="steps">
        <li v-for="(s, si) in review" :key="s.title">
          <span class="num">{{ reviewStart + si + 1 }}</span>
          <div class="s-body">
            <span class="s-title">{{ s.title }}</span>
            <!-- eslint-disable-next-line vue/no-v-html -- 內容寫死在本檔 -->
            <span class="s-text" v-html="s.body" />
          </div>
        </li>
      </ol>
    </section>
  </div>
</template>

<style scoped lang="scss">
.guide {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.block {
  padding: 16px 18px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  h2 {
    margin: 0;
    font-size: 16px;
  }
}
.lede {
  margin: 0;
  color: var(--lc-muted);
  font-size: 14px;
}
.owners {
  display: grid;
  gap: 0;
}
.owner {
  display: grid;
  grid-template-columns: 150px 150px minmax(0, 1fr);
  gap: 10px;
  padding: 6px 0;
  border-bottom: 1px solid var(--lc-border);
  font-size: 13px;
  &:last-child {
    border-bottom: 0;
  }
}
.o-item {
  color: var(--lc-muted);
}
.o-who {
  font-weight: 600;
}
.o-note {
  color: var(--lc-muted);
}
.phase-head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 6px 10px;
}
.who {
  font-size: 12px;
  padding: 1px 10px;
  border-radius: 999px;
  background: var(--lc-tone-caramel-bg);
  color: var(--lc-tone-caramel-fg);
}
.where {
  font-size: 12px;
  color: var(--lc-muted);
}
.steps {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: 10px;
  li {
    display: grid;
    grid-template-columns: 26px minmax(0, 1fr);
    gap: 10px;
  }
}
.num {
  width: 24px;
  height: 24px;
  border-radius: 50%;
  display: grid;
  place-items: center;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  border: 1px solid var(--lc-border);
  color: var(--lc-muted);
}
.s-body {
  display: grid;
  gap: 3px;
  min-width: 0;
}
.s-title {
  font-weight: 600;
  font-size: 14px;
}
.s-text {
  font-size: 13px;
  line-height: 1.65;
  color: var(--lc-muted);
  margin: 0;
  :deep(b) {
    color: var(--lc-text);
    font-weight: 500;
  }
  :deep(code) {
    font-size: 12px;
    padding: 0 5px;
    border-radius: 4px;
    background: var(--lc-bg-soft);
    overflow-wrap: anywhere;
  }
}
.note {
  font-size: 12px;
  padding: 6px 10px;
  border-radius: 8px;
  background: var(--lc-warn-bg);
  color: var(--lc-warn-fg);
}
.fork {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 8px;
  > div {
    padding: 8px 12px;
    border-radius: 8px;
    font-size: 13px;
    display: grid;
    gap: 2px;
  }
  .yes {
    background: var(--lc-tone-sage-bg);
    color: var(--lc-tone-sage-fg);
  }
  .no {
    background: var(--lc-warn-bg);
    color: var(--lc-warn-fg);
  }
}
@media (max-width: 768px) {
  .owner {
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    .o-note {
      grid-column: 1 / -1;
    }
  }
}

@media (max-width: 768px) {
  // 手機字級 ≥13px（桌機維持原值）
  .who,
  .where,
  .num,
  .s-text :deep(code),
  .note {
    font-size: 13px;
  }
}
</style>
