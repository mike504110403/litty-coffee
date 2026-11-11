import { http } from './http'

export type PostKind = 'image' | 'carousel' | 'reel' | 'story'
export type PostStatus = 'draft' | 'scheduled' | 'publishing' | 'published' | 'failed'
export type SaveAction = 'draft' | 'schedule' | 'now'

export interface MediaAsset {
  id: number
  igAccountId: number // 上傳時選的帳號；只能用在該帳號的貼文
  filename: string
  originalName: string
  contentType: string
  kind: 'image' | 'video'
  sizeBytes: number
  width: number
  height: number
  url: string
}

export interface Post {
  id: number
  igAccountId: number // 發佈到哪個 IG 帳號
  kind: PostKind
  caption: string
  mediaIds: number[]
  media: MediaAsset[]
  scheduledAt: string | null
  status: PostStatus
  attempts: number
  nextAttemptAt: string | null
  permalink: string
  error: string
  publishedAt: string | null
  updatedAt: string
  /** IG 成效快照；key 依貼文類型而異（限動沒有 likes／comments／saved，改有 replies） */
  insights: Partial<Record<InsightKey, number>>
  insightsFetchedAt: string | null
  insightsError: string
  /** 目前能不能手動更新（限動超過 24 小時就不行） */
  insightsRefreshable: boolean
}

export type InsightKey = 'views' | 'reach' | 'likes' | 'comments' | 'saved' | 'shares' | 'replies'

export const insightLabels: Record<InsightKey, string> = {
  views: '觀看',
  reach: '觸及',
  likes: '按讚',
  comments: '留言',
  saved: '收藏',
  shares: '分享',
  replies: '回覆',
}

/** 各貼文類型顯示的指標與順序 */
export const insightKeysOf = (kind: PostKind): InsightKey[] =>
  kind === 'story'
    ? ['views', 'reach', 'replies', 'shares']
    : ['views', 'reach', 'likes', 'comments', 'saved', 'shares']

/** 互動數＝按讚＋留言＋收藏＋分享（限動為回覆＋分享） */
export const interactions = (p: Post) => {
  const i = p.insights ?? {}
  return (i.likes ?? 0) + (i.comments ?? 0) + (i.saved ?? 0) + (i.shares ?? 0) + (i.replies ?? 0)
}

export const kindLabels: Record<PostKind, string> = {
  image: '貼文',
  carousel: '貼文', // 與單張合併顯示為「貼文」
  reel: 'Reels',
  story: '限動',
}

export const statusLabels: Record<PostStatus, string> = {
  draft: '草稿',
  scheduled: '已排程',
  publishing: '發佈中',
  published: '已發佈',
  failed: '失敗',
}

export const MAX_CAPTION = 2200
export const MAX_HASHTAGS = 30
export const countHashtags = (s: string) => (s.match(/#[\p{L}\p{N}_]+/gu) ?? []).length

export const postsApi = {
  list: (params: { from?: string; to?: string; status?: string; account?: number } = {}) =>
    http.get<Post[]>('/posts', { params }).then((r) => r.data),
  upcomingCount: (account?: number) =>
    http.get<{ count: number }>('/posts/upcoming-count', { params: { account } }).then((r) => r.data.count),
  save: (post: Partial<Post> & { kind: PostKind }, action: SaveAction) =>
    (post.id ? http.put<Post>(`/posts/${post.id}`, { post, action }) : http.post<Post>('/posts', { post, action })).then(
      (r) => r.data,
    ),
  refreshInsights: (id: number) =>
    http.post<Post>(`/posts/${id}/insights/refresh`, null, { timeout: 30000 }).then((r) => r.data),
  remove: (id: number) => http.delete(`/posts/${id}`),
  upload: (account: number, file: File, onProgress?: (pct: number) => void) => {
    const fd = new FormData()
    fd.append('file', file)
    fd.append('account', String(account))
    return http
      .post<MediaAsset>('/media', fd, {
        timeout: 300000,
        onUploadProgress: (e) => e.total && onProgress?.(Math.round((e.loaded / e.total) * 100)),
      })
      .then((r) => r.data)
  },
  // mediaIds：貼文圖片（依順序），後端會一併給 AI 看；brief 可空（只看圖片寫）
  caption: (account: number, brief: string, kind: PostKind, mediaIds: number[] = []) =>
    http
      .post<{ caption: string; hashtags: number }>('/posts/caption', { brief, kind, account, mediaIds }, { timeout: 90000 })
      .then((r) => r.data),
}
