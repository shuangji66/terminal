// API client. The frontend is served over the unix admin socket under a baseurl
// prefix fronted by a proxy. The backend injects a <base href> tag into
// index.html at runtime with the real baseurl (TERMINAL_ADMIN_BASEURL), so we
// resolve API/WS paths against document.baseURI — the prefix is NOT known at
// build time.
export function runtimeBase(): string {
  if (typeof document !== 'undefined' && document.baseURI) {
    const p = new URL(document.baseURI).pathname
    return p.endsWith('/') ? p.slice(0, -1) : p
  }
  return import.meta.env.BASE_URL.replace(/\/$/, '') || ''
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(runtimeBase() + path, {
    headers: { 'Content-Type': 'application/json' },
    ...init
  })
  const data = await res.json().catch(() => null)
  if (!res.ok || !data || data.ok === false) {
    throw new Error((data && (data.error || data.msg)) || `HTTP ${res.status}`)
  }
  return data as T
}

// wsUrl builds the WebSocket URL for the terminal endpoint, under the runtime
// base path. id 为空时后端新建会话，用户由 user 参数决定。
// user 参数："root" → 以 root 运行；"app:<APP NAME>" → 以该 NAS 应用用户运行
// （HOME 与工作目录均为 /var/apps/<APP NAME>/home）；省略 → 当前登录用户
// （后端读 X-Trim-Userid）。
export function wsUrl(id?: string, user?: string): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  let query = ''
  if (id) query = `?id=${encodeURIComponent(id)}`
  else if (user) query = `?user=${encodeURIComponent(user)}`
  const u = new URL(runtimeBase() + '/terminal' + query, location.href)
  u.protocol = proto
  return u.toString()
}

// 与后端约定的 OSC 控制消息（不写入 PTY）
export function resizePayload(cols: number, rows: number): string {
  return `\x1b]resize;${cols};${rows}\x07`
}
export const HEARTBEAT_PAYLOAD = '\x1b]ping\x07'

// 会话被其他设备接管时，后端先发该控制帧再以 WS_CLOSE_DETACHED 关闭连接（双保险）。
export const DETACHED_PAYLOAD = '\x1b]detached\x07'
// 「已被其他设备接管」的 WebSocket 关闭码（与 backend/terminal.go 的 wsCloseTaken 对应）
export const WS_CLOSE_DETACHED = 4001

export interface QuickCmd {
  id: string
  name: string
  content: string
  auto: boolean
}

export interface SessionInfo {
  id: string
  createdAt: string
  lastActive: string
  size: number
  exited: boolean
  /** 会话创建时请求的 user= 参数（'' = 登录用户 / root / app:<APP NAME>）；老后端可能没有 */
  userSpec?: string
  // 会话实际运行用户的显示名（root / NAS 用户名 / 应用 APP NAME），用于标签上的用户标注
  user?: string
}

export interface RuntimeInfo {
  adminSock: string
  adminBaseURL: string
  sessionDir: string
  quickCmdsFile: string
  shell: string
  home: string
  version: string
  lang: string
  trimUid?: string // 网关 X-Trim-Userid 原始值
  nasUser?: NasUserInfo
  currentUid?: number
}

export interface NasUserInfo {
  uid: number
  gid: number
  username: string
  home: string
}

// 单个标签会话的运行用户："nas"（登录用户）/ "root" / "app:<APP NAME>"（NAS 应用用户）
export type UserSpec = 'nas' | 'root' | `app:${string}`

// 新建终端时可选的 NAS 应用用户（APP NAME = 系统里的应用用户名）
export interface AppUserInfo {
  name: string
  displayName?: string
}

export const api = {
  info: () => request<{ ok: boolean; runtime: RuntimeInfo }>('/api/info'),
  sessions: () => request<{ ok: boolean; sessions: SessionInfo[] }>('/api/sessions'),
  // 应用用户列表（appcenter-cli list 解析结果，后端已过滤 trim.* 与不可用项）
  appUsers: () =>
    request<{ ok: boolean; apps: AppUserInfo[]; homeTemplate: string }>('/api/apps'),
  closeSession: (id: string) =>
    request<{ ok: boolean; id: string }>('/api/session?id=' + encodeURIComponent(id), { method: 'DELETE' }),
  clearSessionHistory: (id: string) =>
    request<{ ok: boolean; id: string }>('/api/session/clear?id=' + encodeURIComponent(id), { method: 'POST' }),
  listQuickCmds: () => request<{ ok: boolean; path: string; commands: QuickCmd[] }>('/api/quickcmds'),
  saveQuickCmds: (commands: QuickCmd[]) =>
    request<{ ok: boolean; path: string; commands: QuickCmd[] }>('/api/quickcmds', {
      method: 'POST',
      body: JSON.stringify({ commands })
    })
}