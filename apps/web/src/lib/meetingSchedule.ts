import type { EmployeeMeeting } from './api'

export type MeetingStatus = 'scheduled' | 'live' | 'ended'

/** datetime-local 值转为 RFC3339（浏览器本地时区）。 */
export function datetimeLocalToRFC3339(value: string): string | null {
  const trimmed = value.trim()
  if (!trimmed) return null
  const date = new Date(trimmed)
  if (Number.isNaN(date.getTime())) return null
  return date.toISOString()
}

export function meetingStatusLabel(m: EmployeeMeeting): string {
  const status = resolveMeetingStatus(m)
  if (status === 'ended') return '已结束'
  if (status === 'scheduled') return '待开始'
  if (m.locked) return '已锁定'
  return '进行中'
}

export function resolveMeetingStatus(m: {
  status?: MeetingStatus
  ended?: boolean
  starts_at?: string | null
}): MeetingStatus {
  if (m.status === 'scheduled' || m.status === 'live' || m.status === 'ended') {
    return m.status
  }
  if (m.ended) return 'ended'
  if (m.starts_at && new Date(m.starts_at).getTime() > Date.now()) return 'scheduled'
  return 'live'
}

/** Aura 列表状态徽标样式：scheduled / live / ended */
export function meetingStatusBadgeClass(status: MeetingStatus): string {
  if (status === 'scheduled') return 'text-secondary'
  if (status === 'live') return 'text-accent'
  return 'text-secondary/60'
}

export function meetingStatusBadgeText(status: MeetingStatus): string {
  if (status === 'scheduled') return '待开始'
  if (status === 'live') return '进行中'
  return '已结束'
}

export function hasMeetingStarted(startsAtISO?: string | null, nowMs = Date.now()): boolean {
  if (!startsAtISO) return true
  return new Date(startsAtISO).getTime() <= nowMs
}

/** 创建表单本地校验：starts_at 须在未来；ends_at 须严格晚于 starts_at。 */
export function isScheduleCreateValid(
  startsAtLocal: string,
  endsAtLocal: string,
  nowMs = Date.now(),
): boolean {
  const starts = startsAtLocal.trim()
  const ends = endsAtLocal.trim()
  if (!starts) return !ends
  const startsMs = new Date(starts).getTime()
  if (Number.isNaN(startsMs) || startsMs <= nowMs) return false
  if (!ends) return true
  const endsMs = new Date(ends).getTime()
  return !Number.isNaN(endsMs) && endsMs > startsMs
}

export function formatCountdown(targetISO: string, nowMs = Date.now()): string {
  const diff = new Date(targetISO).getTime() - nowMs
  if (diff <= 0) return '即将开始'
  const totalSec = Math.ceil(diff / 1000)
  const hours = Math.floor(totalSec / 3600)
  const minutes = Math.floor((totalSec % 3600) / 60)
  const seconds = totalSec % 60
  if (hours > 0) {
    return `${hours} 小时 ${minutes} 分 ${seconds} 秒`
  }
  if (minutes > 0) {
    return `${minutes} 分 ${seconds} 秒`
  }
  return `${seconds} 秒`
}
