/**
 * 员工桌面端本机麦克风备份（架构 §5）。
 * 只在 Tauri WebView 里有意义；普通浏览器没有这些 invoke。
 */
import { invoke } from '@tauri-apps/api/core'
import { isTauriRuntime } from './client'

export type MicStatus = {
  active: boolean
  sample_rate: number
  channels: number
  error: string | null
}

export type StartLocalRecordingResult = {
  state: string
  upload_id: string
  spool_dir: string
  mic: MicStatus
}

export type StopLocalRecordingResult = {
  state: string
  parts: number
  checksum: string
  bytes: number
  audit_flushed: number
  /** 网关落点：s3 / local_spool / local_spool_only */
  stored_in?: string
  object_key?: string
}

/** 网关基址：Rust 上传不走 Vite 代理，必须直连网关端口。 */
export function gatewayBaseUrl(): string {
  const fromEnv = import.meta.env.VITE_GATEWAY_BASE_URL as string | undefined
  if (fromEnv && fromEnv.trim()) {
    return fromEnv.replace(/\/$/, '')
  }
  return 'http://127.0.0.1:18080'
}

export function canUseLocalRecording(): boolean {
  if (!isTauriRuntime()) return false
  // 嘉宾禁止本机录音（架构：嘉宾只有确认告知，没有本地备份）。
  return sessionStorage.getItem('principalKind') === 'employee'
}

export async function startLocalRecording(
  meetingId: string,
  employeeJwt: string,
): Promise<StartLocalRecordingResult> {
  return invoke('start_local_recording', {
    meetingId,
    gatewayBaseUrl: gatewayBaseUrl(),
    employeeJwt,
  })
}

export async function pauseLocalRecording(): Promise<string> {
  return invoke('pause_local_recording')
}

export async function resumeLocalRecording(): Promise<string> {
  return invoke('resume_local_recording')
}

export async function stopAndUploadLocalRecording(): Promise<StopLocalRecordingResult> {
  return invoke('stop_and_upload_local_recording')
}

export async function recordingState(): Promise<string> {
  return invoke('recording_state')
}

export async function purgeLocalRecording(): Promise<string> {
  return invoke('purge_local_recording')
}

/** 手动重试：把尚未回传的本机审计推给网关。 */
export async function flushRecordingAudit(): Promise<number> {
  return invoke('flush_recording_audit')
}

export type PendingUpload = {
  meeting_id: string
  upload_id: string
  gateway_base: string
  spool_dir: string
  encrypted_path: string
  updated_at_ms: number
}

export async function listPendingUploads(): Promise<PendingUpload[]> {
  return invoke('list_pending_uploads')
}

export async function resumePendingUpload(
  uploadId: string,
  employeeJwt: string,
): Promise<StopLocalRecordingResult> {
  return invoke('resume_pending_upload', {
    uploadId,
    employeeJwt,
  })
}
