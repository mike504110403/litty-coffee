export function formatDuration(secs: number | null | undefined): string {
  if (secs == null) return '—'
  if (secs < 60) return `${Math.round(secs)} 秒`
  if (secs < 3600) return `${Math.round(secs / 60)} 分`
  if (secs < 86400) return `${(secs / 3600).toFixed(1)} 小時`
  return `${(secs / 86400).toFixed(1)} 天`
}

export function formatPercent(v: number | null | undefined): string {
  return v == null ? '—' : `${Math.round(v * 100)}%`
}

export function formatCount(n: number): string {
  return n >= 10000 ? `${(n / 1000).toFixed(1)}K` : n.toLocaleString('zh-TW')
}

/** 與上期相比的變化；回傳 null 表示無從比較 */
export function delta(cur: number | null | undefined, prev: number | null | undefined) {
  if (cur == null || prev == null || prev === 0) return null
  return (cur - prev) / prev
}

export function fullDate(iso: string): string {
  const d = new Date(iso)
  return `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()}`
}
