const pad = (n: number) => String(n).padStart(2, '0')

const sameDay = (a: Date, b: Date) =>
  a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()

/** 列表用：今天顯示時間、昨天顯示「昨天」、今年顯示月/日，其餘顯示年/月/日 */
export function shortTime(iso: string | null): string {
  if (!iso) return ''
  const d = new Date(iso)
  const now = new Date()
  if (sameDay(d, now)) return `${pad(d.getHours())}:${pad(d.getMinutes())}`
  const y = new Date(now)
  y.setDate(now.getDate() - 1)
  if (sameDay(d, y)) return '昨天'
  if (d.getFullYear() === now.getFullYear()) return `${d.getMonth() + 1}/${d.getDate()}`
  return `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()}`
}

export function clockTime(iso: string): string {
  const d = new Date(iso)
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 對話中的日期分隔線 */
export function dayLabel(iso: string): string {
  const d = new Date(iso)
  const now = new Date()
  if (sameDay(d, now)) return '今天'
  const y = new Date(now)
  y.setDate(now.getDate() - 1)
  if (sameDay(d, y)) return '昨天'
  const wd = '日一二三四五六'[d.getDay()]
  return `${d.getMonth() + 1} 月 ${d.getDate()} 日（${wd}）`
}

export function dayKey(iso: string): string {
  const d = new Date(iso)
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`
}
