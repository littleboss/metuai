/** 入会阶段 LiveKit 错误分类：设备权限 vs 信令/网络。 */
export type JoinErrorKind = 'permission' | 'signal'

/** 信令/网络类关键词（含 compose 下常见的 Failed to fetch）。 */
export function classifyJoinError(message: string): JoinErrorKind {
  const m = message.toLowerCase()
  if (
    m.includes('could not establish signal connection') ||
    m.includes('failed to fetch') ||
    m.includes('websocket') ||
    m.includes('signal connection') ||
    m.includes('connection refused') ||
    m.includes('networkerror') ||
    m.includes('err_connection')
  ) {
    return 'signal'
  }
  return 'permission'
}

export function joinErrorBanner(kind: JoinErrorKind, detail: string): string {
  if (kind === 'signal') {
    return `连不上媒体服务（${detail}）。请确认 LiveKit 已启动且地址可达。`
  }
  return `设备权限受限（${detail}），仍可入会；画面将显示「无画面/无声」。`
}

/** 供契约检查：信令错误不得与「设备权限受限」同屏。 */
export function isPermissionBannerCopy(copy: string): boolean {
  return copy.includes('设备权限受限')
}
