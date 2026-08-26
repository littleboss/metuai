export type CreatedMeeting = {
  id: string
  title: string
  password: string
  organizer_id: string
  locked?: boolean
  ended?: boolean
}

export type EmployeeMeeting = {
  id: string
  title: string
  organizer_id: string
  locked: boolean
  ended: boolean
  pipeline_stage: string
  created_at: string
}

type GuestSession = {
  token: string
  guest_id: string
}

export type LiveKitCredentials = {
  token: string
  livekit_url: string
  identity: string
  is_organizer: boolean
  meeting_id: string
  organizer_id: string
}

export type ChatMessage = {
  id: string
  meeting_id: string
  sender_key: string
  display_name: string
  body: string
  created_at: string
}

import { metuaiClientHeaders } from './client'

// 将网关返回的正文保留在错误中，便于页面展示具体失败原因。
async function assertOk(response: Response): Promise<void> {
  if (!response.ok) {
    const message = await response.text()
    throw new Error(message || `请求失败（HTTP ${response.status}）`)
  }
}

function authHeaders(token: string, json = false): HeadersInit {
  const headers: Record<string, string> = {
    Authorization: `Bearer ${token}`,
    ...metuaiClientHeaders(),
  }
  if (json) {
    headers['Content-Type'] = 'application/json'
  }
  return headers
}

export async function createMeeting(
  employeeToken: string,
  title: string,
  employeeIds: string[] = [],
  coOrganizerIds: string[] = [],
): Promise<CreatedMeeting> {
  const response = await fetch('/v1/meetings', {
    method: 'POST',
    headers: authHeaders(employeeToken, true),
    body: JSON.stringify({
      title,
      employee_ids: employeeIds,
      co_organizer_ids: coOrganizerIds,
    }),
  })

  await assertOk(response)
  return response.json() as Promise<CreatedMeeting>
}

export async function listEmployeeMeetings(employeeToken: string): Promise<EmployeeMeeting[]> {
  const response = await fetch('/v1/meetings', {
    method: 'GET',
    headers: authHeaders(employeeToken),
  })
  await assertOk(response)
  const payload = (await response.json()) as { meetings: EmployeeMeeting[] }
  return payload.meetings ?? []
}

export async function guestSession(
  meetingId: string,
  password: string,
  displayName: string,
): Promise<GuestSession> {
  const response = await fetch(`/v1/meetings/${meetingId}/guest-session`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      password,
      display_name: displayName,
    }),
  })

  await assertOk(response)
  return response.json() as Promise<GuestSession>
}

export async function requestGuestEmailVerification(
  meetingId: string,
  token: string,
  email: string,
): Promise<{ expires_at: string }> {
  const response = await fetch(`/v1/meetings/${meetingId}/guest-email-verification`, {
    method: 'POST',
    headers: authHeaders(token, true),
    body: JSON.stringify({ email }),
  })
  await assertOk(response)
  return response.json() as Promise<{ expires_at: string }>
}

export async function confirmGuestEmailVerification(
  meetingId: string,
  token: string,
  email: string,
  code: string,
): Promise<{ access_token: string; email: string }> {
  const response = await fetch(`/v1/meetings/${meetingId}/guest-email-verification/confirm`, {
    method: 'POST',
    headers: authHeaders(token, true),
    body: JSON.stringify({ email, code }),
  })
  await assertOk(response)
  return response.json() as Promise<{ access_token: string; email: string }>
}

export async function ackRecording(
  meetingId: string,
  token: string,
  password?: string,
): Promise<void> {
  const response = await fetch(`/v1/meetings/${meetingId}/recording-ack`, {
    method: 'POST',
    headers: authHeaders(token, Boolean(password)),
    body: password ? JSON.stringify({ password }) : undefined,
  })

  await assertOk(response)
}

export async function livekitToken(
  meetingId: string,
  token: string,
): Promise<LiveKitCredentials> {
  const response = await fetch(`/v1/meetings/${meetingId}/livekit-token`, {
    method: 'POST',
    headers: authHeaders(token),
  })

  await assertOk(response)
  return response.json() as Promise<LiveKitCredentials>
}

export async function lockMeeting(meetingId: string, token: string): Promise<void> {
  const response = await fetch(`/v1/meetings/${meetingId}/lock`, {
    method: 'POST',
    headers: authHeaders(token),
  })
  await assertOk(response)
}

export async function unlockMeeting(meetingId: string, token: string): Promise<void> {
  const response = await fetch(`/v1/meetings/${meetingId}/unlock`, {
    method: 'POST',
    headers: authHeaders(token),
  })
  await assertOk(response)
}

export async function endMeeting(meetingId: string, token: string): Promise<void> {
  const response = await fetch(`/v1/meetings/${meetingId}/end`, {
    method: 'POST',
    headers: authHeaders(token),
  })
  await assertOk(response)
}

export async function resetPassword(
  meetingId: string,
  token: string,
): Promise<{ password: string }> {
  const response = await fetch(`/v1/meetings/${meetingId}/reset-password`, {
    method: 'POST',
    headers: authHeaders(token),
  })
  await assertOk(response)
  return response.json() as Promise<{ password: string }>
}

export async function kickParticipant(
  meetingId: string,
  token: string,
  identity: string,
): Promise<void> {
  const response = await fetch(`/v1/meetings/${meetingId}/kick`, {
    method: 'POST',
    headers: authHeaders(token, true),
    body: JSON.stringify({ identity }),
  })
  await assertOk(response)
}

export async function postChat(
  meetingId: string,
  token: string,
  body: string,
): Promise<ChatMessage> {
  const response = await fetch(`/v1/meetings/${meetingId}/chat`, {
    method: 'POST',
    headers: authHeaders(token, true),
    body: JSON.stringify({ body }),
  })
  await assertOk(response)
  return response.json() as Promise<ChatMessage>
}

export async function listChat(
  meetingId: string,
  token: string,
): Promise<ChatMessage[]> {
  const response = await fetch(`/v1/meetings/${meetingId}/chat`, {
    method: 'GET',
    headers: authHeaders(token),
  })
  await assertOk(response)
  const payload = (await response.json()) as { messages: ChatMessage[] }
  return payload.messages ?? []
}

export async function heartbeat(meetingId: string, token: string): Promise<void> {
  const response = await fetch(`/v1/meetings/${meetingId}/heartbeat`, {
    method: 'POST',
    headers: authHeaders(token),
  })
  await assertOk(response)
}

export type PipelineStatus = {
  meeting_id: string
  title: string
  ended: boolean
  pipeline_stage: string
}

export type TranscriptSegment = {
  id: string
  meeting_id: string
  speaker_display_name: string
  start_ms: number
  end_ms: number
  text: string
  source: string
  asr_model: string
}

export type MeetingSummary = {
  meeting_id: string
  summary: string
  decisions: string[]
  action_items: string[]
  risks: string[]
  open_questions: string[]
  created_at: string
}

export async function getPipeline(
  meetingId: string,
  token: string,
): Promise<PipelineStatus> {
  const response = await fetch(`/v1/meetings/${meetingId}/pipeline`, {
    method: 'GET',
    headers: authHeaders(token),
  })
  await assertOk(response)
  return response.json() as Promise<PipelineStatus>
}

export async function runFakePipeline(
  meetingId: string,
  token: string,
): Promise<{ pipeline_stage: string }> {
  const response = await fetch(`/v1/meetings/${meetingId}/pipeline/run-fake`, {
    method: 'POST',
    headers: authHeaders(token),
  })
  await assertOk(response)
  return response.json() as Promise<{ pipeline_stage: string }>
}

/** 网关内 stub ASR → TRANSCRIPT_READY（不启 Python / FunASR）。 */
export async function runAsrStub(
  meetingId: string,
  token: string,
): Promise<{ pipeline_stage: string; backend: string }> {
  const response = await fetch(`/v1/meetings/${meetingId}/pipeline/run-asr-stub`, {
    method: 'POST',
    headers: authHeaders(token),
  })
  await assertOk(response)
  return response.json() as Promise<{ pipeline_stage: string; backend: string }>
}

export async function getTranscript(
  meetingId: string,
  token: string,
): Promise<TranscriptSegment[]> {
  const response = await fetch(`/v1/meetings/${meetingId}/transcript`, {
    method: 'GET',
    headers: authHeaders(token),
  })
  await assertOk(response)
  const payload = (await response.json()) as { segments: TranscriptSegment[] }
  return payload.segments ?? []
}

export async function getSummary(
  meetingId: string,
  token: string,
): Promise<MeetingSummary> {
  const response = await fetch(`/v1/meetings/${meetingId}/summary`, {
    method: 'GET',
    headers: authHeaders(token),
  })
  await assertOk(response)
  return response.json() as Promise<MeetingSummary>
}

export type KnowledgeHit = {
  document: {
    meeting_id: string
    title: string
    text: string
    source_type: string
  }
  snippet: string
}

/** 知识检索（ACL 在索引侧按组织者/嘉宾邮箱过滤）。 */
export async function searchKnowledge(
  query: string,
  token: string,
): Promise<{ query: string; backend: string; hits: KnowledgeHit[] }> {
  const response = await fetch(`/v1/knowledge/search?q=${encodeURIComponent(query)}`, {
    method: 'GET',
    headers: authHeaders(token),
  })
  await assertOk(response)
  return response.json() as Promise<{ query: string; backend: string; hits: KnowledgeHit[] }>
}

export async function reindexMeetingKnowledge(
  meetingId: string,
  token: string,
): Promise<{ ok: boolean; backend: string }> {
  const response = await fetch(`/v1/meetings/${meetingId}/knowledge/index`, {
    method: 'POST',
    headers: authHeaders(token),
  })
  await assertOk(response)
  return response.json() as Promise<{ ok: boolean; backend: string }>
}

/** 显式下载产物并写入 artifact_download 审计。 */
export async function downloadArtifact(
  meetingId: string,
  token: string,
  kind: 'transcript' | 'summary' | 'media',
  artifactId?: string,
): Promise<{ kind: string; audited: boolean }> {
  const response = await fetch(`/v1/meetings/${meetingId}/artifacts/download`, {
    method: 'POST',
    headers: authHeaders(token, true),
    body: JSON.stringify({ kind, artifact_id: artifactId ?? '' }),
  })
  await assertOk(response)
  return response.json() as Promise<{ kind: string; audited: boolean }>
}

export type BreakGlassRequest = {
  id: string
  meeting_id: string
  applicant: string
  reason: string
  status: string
  approver?: string
  expires_at?: string
}

export async function applyBreakGlass(
  meetingId: string,
  token: string,
  reason: string,
): Promise<BreakGlassRequest> {
  const response = await fetch(`/v1/meetings/${meetingId}/break-glass`, {
    method: 'POST',
    headers: authHeaders(token, true),
    body: JSON.stringify({ reason }),
  })
  await assertOk(response)
  return response.json() as Promise<BreakGlassRequest>
}

export async function approveBreakGlass(
  meetingId: string,
  reqId: string,
  token: string,
): Promise<BreakGlassRequest> {
  const response = await fetch(`/v1/meetings/${meetingId}/break-glass/${reqId}/approve`, {
    method: 'POST',
    headers: authHeaders(token),
  })
  await assertOk(response)
  return response.json() as Promise<BreakGlassRequest>
}

export async function listBreakGlass(
  meetingId: string,
  token: string,
): Promise<BreakGlassRequest[]> {
  const response = await fetch(`/v1/meetings/${meetingId}/break-glass`, {
    method: 'GET',
    headers: authHeaders(token),
  })
  await assertOk(response)
  const payload = (await response.json()) as { requests: BreakGlassRequest[] }
  return payload.requests ?? []
}

export type MediaArtifact = {
  id: string
  meeting_id: string
  kind: string
  status: string
  object_key: string
  detail: string
  egress_id: string
}

export async function getMedia(
  meetingId: string,
  token: string,
): Promise<MediaArtifact[]> {
  const response = await fetch(`/v1/meetings/${meetingId}/media`, {
    method: 'GET',
    headers: authHeaders(token),
  })
  await assertOk(response)
  const payload = (await response.json()) as { artifacts: MediaArtifact[] }
  return payload.artifacts ?? []
}

export type AuditEvent = {
  id: string
  meeting_id: string
  actor_key: string
  action: string
  detail: string
  created_at: string
}

export async function getAudit(
  meetingId: string,
  token: string,
): Promise<AuditEvent[]> {
  const response = await fetch(`/v1/meetings/${meetingId}/audit`, {
    method: 'GET',
    headers: authHeaders(token),
  })
  await assertOk(response)
  const payload = (await response.json()) as { events: AuditEvent[] }
  return payload.events ?? []
}
