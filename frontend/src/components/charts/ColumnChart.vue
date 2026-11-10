<script setup lang="ts">
import { computed, ref } from 'vue'

export interface ColumnSeries {
  key: string
  label: string
  color: string
  values: number[]
}

const props = withDefaults(
  defineProps<{
    categories: string[] // x 軸完整標籤（tooltip 用）
    shortLabels?: string[] // x 軸顯示用
    series: ColumnSeries[] // 多個系列時堆疊
    height?: number
    description: string // 給螢幕閱讀器的圖表說明
    showTable?: boolean
  }>(),
  { height: 200, shortLabels: undefined, showTable: false },
)

// 每根柱子只畫大於 0 的段（讓 :last-child 一定是最上面那段）
const stacks = computed(() =>
  props.categories.map((_, i) =>
    props.series.filter((s) => (s.values[i] ?? 0) > 0).map((s) => ({ key: s.key, color: s.color, value: s.values[i] })),
  ),
)
const totals = computed(() => props.categories.map((_, i) => props.series.reduce((n, s) => n + (s.values[i] ?? 0), 0)))

// 乾淨的刻度：1/2/5 × 10^n
const ticks = computed(() => {
  const max = Math.max(...totals.value, 0)
  if (max === 0) return [0, 1]
  const rough = max / 4
  const pow = 10 ** Math.floor(Math.log10(rough))
  const step = Math.max(1, [1, 2, 5, 10].map((m) => m * pow).find((s) => s >= rough) ?? pow * 10)
  const top = Math.ceil(max / step) * step
  const out: number[] = []
  for (let v = 0; v <= top + 1e-9; v += step) out.push(Math.round(v))
  return out
})
const top = computed(() => ticks.value[ticks.value.length - 1] || 1)

// x 標籤抽樣，避免擠在一起
const labelEvery = computed(() => {
  const n = props.categories.length
  return n <= 8 ? 1 : n <= 16 ? 2 : n <= 31 ? 5 : Math.ceil(n / 8)
})
const xLabel = (i: number) => (props.shortLabels ?? props.categories)[i]

const hover = ref<number | null>(null)
const tipLeft = computed(() => (hover.value == null ? 0 : ((hover.value + 0.5) / props.categories.length) * 100))

const multi = computed(() => props.series.length > 1)
</script>

<template>
  <div class="column-chart">
    <div v-if="multi" class="legend">
      <span v-for="s in series" :key="s.key" class="key"><i :style="{ background: s.color }" />{{ s.label }}</span>
    </div>

    <table v-if="showTable" class="data-table">
      <thead>
        <tr>
          <th />
          <th v-for="s in series" :key="s.key">{{ s.label }}</th>
          <th v-if="multi">合計</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(c, i) in categories" :key="c">
          <th>{{ c }}</th>
          <td v-for="s in series" :key="s.key">{{ s.values[i] }}</td>
          <td v-if="multi">{{ totals[i] }}</td>
        </tr>
      </tbody>
    </table>

    <div v-else class="plot" :style="{ height: `${height}px` }" role="img" :aria-label="description">
      <div class="y-axis">
        <span v-for="t in ticks" :key="t" :style="{ bottom: `${(t / top) * 100}%` }">{{ t }}</span>
      </div>
      <div class="area" @mouseleave="hover = null">
        <div v-for="t in ticks" :key="t" class="grid" :style="{ bottom: `${(t / top) * 100}%` }" />
        <div class="cols">
          <div
            v-for="(c, i) in categories"
            :key="c"
            class="slot"
            :class="{ dim: hover != null && hover !== i }"
            @mouseenter="hover = i"
            @click="hover = hover === i ? null : i"
          >
            <div class="stack" :style="{ height: `${(totals[i] / top) * 100}%` }">
              <div
                v-for="seg in stacks[i]"
                :key="seg.key"
                class="seg"
                :style="{ flexGrow: seg.value, background: seg.color }"
              />
            </div>
          </div>
        </div>
        <div v-if="hover != null" class="tip" :class="{ right: tipLeft > 60 }" :style="{ left: `${tipLeft}%` }">
          <div class="tip-title">{{ categories[hover] }}</div>
          <div v-for="s in series" :key="s.key" class="tip-row">
            <i v-if="multi" :style="{ background: s.color }" />
            <span>{{ s.label }}</span>
            <b>{{ s.values[hover] }}</b>
          </div>
        </div>
      </div>
      <div class="x-axis">
        <span v-for="(c, i) in categories" :key="c">{{ i % labelEvery === 0 || i === categories.length - 1 ? xLabel(i) : '' }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped lang="scss">
.legend {
  display: flex;
  gap: 14px;
  font-size: 12px;
  color: var(--lc-muted);
  margin-bottom: 10px;
  .key {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  i {
    width: 10px;
    height: 10px;
    border-radius: 3px;
  }
}
.plot {
  position: relative;
  display: grid;
  grid-template-columns: 32px 1fr;
  grid-template-rows: 1fr 20px;
  margin-top: 6px;
}
.y-axis {
  position: relative;
  span {
    position: absolute;
    right: 8px;
    transform: translateY(50%);
    font-size: 11px;
    color: #898781;
    font-variant-numeric: tabular-nums;
  }
}
.area {
  position: relative;
  border-bottom: 1px solid #c3c2b7;
}
.grid {
  position: absolute;
  left: 0;
  right: 0;
  height: 1px;
  background: #ecebe5;
}
.cols {
  position: absolute;
  inset: 0;
  display: flex;
}
.slot {
  flex: 1;
  height: 100%;
  display: flex;
  align-items: flex-end;
  justify-content: center;
  cursor: default;
  transition: opacity 0.12s;
  &.dim {
    opacity: 0.45;
  }
}
.stack {
  width: min(24px, 62%);
  display: flex;
  flex-direction: column-reverse; // 第一個系列貼底
  gap: 2px; // 堆疊段之間的 surface gap
  min-height: 0;
}
.seg {
  flex-basis: 0;
  min-height: 2px;
  &:last-child {
    border-radius: 4px 4px 0 0; // 只有頂端是圓角，貼底是方的
  }
}
.x-axis {
  grid-column: 2;
  display: flex;
  span {
    flex: 1;
    text-align: center;
    font-size: 11px;
    color: #898781;
    padding-top: 4px;
    white-space: nowrap;
    overflow: visible;
  }
}
.tip {
  position: absolute;
  top: 0;
  transform: translateX(12px);
  z-index: 2;
  min-width: 150px;
  white-space: nowrap;
  padding: 8px 10px;
  border-radius: 10px;
  background: #fff;
  box-shadow: 0 4px 16px rgba(59, 42, 32, 0.14);
  border: 1px solid var(--lc-border);
  font-size: 12px;
  pointer-events: none;
  &.right {
    transform: translateX(calc(-100% - 12px));
  }
}
.tip-title {
  font-weight: 700;
  margin-bottom: 4px;
}
.tip-row {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--lc-muted);
  i {
    width: 8px;
    height: 8px;
    border-radius: 2px;
  }
  b {
    margin-left: auto;
    color: var(--lc-text);
    font-variant-numeric: tabular-nums;
  }
}
.data-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
  th,
  td {
    padding: 6px 8px;
    border-bottom: 1px solid var(--lc-border);
    text-align: right;
    font-variant-numeric: tabular-nums;
  }
  th:first-child {
    text-align: left;
    font-weight: 500;
  }
  thead th {
    color: var(--lc-muted);
    font-weight: 500;
  }
}
</style>
