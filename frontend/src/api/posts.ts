import { http } from './http'

export type PostKind = 'image' | 'carousel' | 'reel' | 'story'
export type PostStatus = 'draft' | 'scheduled' | 'publishing' | 'published' | 'failed'
export type SaveAction = 'draft' | 'schedule' | 'now'

export interface MediaAsset {
  id: number
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
}

export const kindLabels: Record<PostKind, string> = {
  image: '貼文',
  carousel: '輪播',
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
  list: (params: { from?: string; to?: string; status?: string } = {}) =>
    http.get<Post[]>('/posts', { params }).then((r) => r.data),
  upcomingCount: () => http.get<{ count: number }>('/posts/upcoming-count').then((r) => r.data.count),
  save: (post: Partial<Post> & { kind: PostKind }, action: SaveAction) =>
    (post.id ? http.put<Post>(`/posts/${post.id}`, { post, action }) : http.post<Post>('/posts', { post, action })).then(
      (r) => r.data,
    ),
  remove: (id: number) => http.delete(`/posts/${id}`),
  upload: (file: File, onProgress?: (pct: number) => void) => {
    const fd = new FormData()
    fd.append('file', file)
    return http
      .post<MediaAsset>('/media', fd, {
        timeout: 300000,
        onUploadProgress: (e) => e.total && onProgress?.(Math.round((e.loaded / e.total) * 100)),
      })
      .then((r) => r.data)
  },
  caption: (brief: string, kind: PostKind) =>
    http
      .post<{ caption: string; hashtags: number }>('/posts/caption', { brief, kind }, { timeout: 90000 })
      .then((r) => r.data),
}
