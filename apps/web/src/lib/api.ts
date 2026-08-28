export type MeetingStatus = 'scheduled' | 'live' | 'ended'

export type CreatedMeeting = {
  id: string
  title: string
  password: string
  organizer_id: string
  locked?: boolean
  ended?: boolean
  starts_at?: string | null
  ends_at?: string | null
  status?: MeetingStatus
}

export type EmployeeMeeting = {
  id: string
  title: string
  organizer_id: string
  locked: boolean
  ended: boolean
  pipeline_stage: string
  created_at: string
  starts_at?: string | null
  ends_at?: string | null
  status?: MeetingStatus
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

export type AuthUser = {
  id: string
  email: string
  display_name: string
}

export type AuthTokens = {
  access_token: string
  user: AuthUser
}

export async function registerAccount(
  email: string,
  password: string,
  displayName?: string,
): Promise<AuthTokens> {
  const response = await fetch('/v1/auth/register', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...metuaiClientHeaders() },
    body: JSON.stringify({
      email,
      password,
      display_name: displayName || undefined,
    }),
  })
  await assertOk(response)
  return response.json() as Promise<AuthTokens>
}

export async function loginAccount(email: string, password: string): Promise<AuthTokens> {
  const response = await fetch('/v1/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...metuaiClientHeaders() },
    body: JSON.stringify({ email, password }),
  })
  await assertOk(response)
  return response.json() as Promise<AuthTokens>
}

export async function createMeeting(
  employeeToken: string,
  title: string,
  employeeIds: string[] = [],
  coOrganizerIds: string[] = [],
  startsAt?: string | null,
  endsAt?: string | null,
): Promise<CreatedMeeting> {
  const response = await fetch('/v1/meetings', {
    method: 'POST',
    headers: authHeaders(employeeToken, true),
    body: JSON.stringify({
      title,
      employee_ids: employeeIds,
      co_organizer_ids: coOrganizerIds,
      starts_at: startsAt ?? undefined,
      ends_at: endsAt ?? undefined,
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

export async function confirmGuestMagicLink(
  meetingId: string,
  magicToken: string,
): Promise<{ access_token: string; email: string }> {
  const response = await fetch(`/v1/meetings/${meetingId}/guest-email-verification/magic`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...metuaiClientHeaders() },
    body: JSON.stringify({ token: magicToken }),
  })
  await assertOk(response)
  return response.json() as Promise<{ access_token: string; email: string }>
}

export type InRoomGuestCode = {
  email: string
  guest_id: string
  code: string
  magic_url: string
  expires_at: string
}

export async function issueInRoomGuestCode(
  meetingId: string,
  token: string,
  email: string,
  guestId = '',
): Promise<InRoomGuestCode> {
  const response = await fetch(`/v1/meetings/${meetingId}/guest-email-verification/in-room`, {
    method: 'POST',
    headers: authHeaders(token, true),
    body: JSON.stringify({ email, guest_id: guestId }),
  })
  await assertOk(response)
  return response.json() as Promise<InRoomGuestCode>
}

export type GuestParticipant = {
  guest_id: string
  display_name: string
}

export async function listGuestParticipants(
  meetingId: string,
  token: string,
): Promise<GuestParticipant[]> {
  const response = await fetch(`/v1/meetings/${meetingId}/guest-participants`, {
    method: 'GET',
    headers: authHeaders(token),
  })
  await assertOk(response)
  const payload = (await response.json()) as {
    guests?: GuestParticipant[]
    guest_ids?: string[]
  }
  if (payload.guests && payload.guests.length > 0) {
    return payload.guests
  }
  return (payload.guest_ids ?? []).map((guestId) => ({
    guest_id: guestId,
    display_name: '',
  }))
}

export async function recordEmployeeLogin(token: string): Promise<void> {
  const response = await fetch('/v1/session/login', {
    method: 'POST',
    headers: authHeaders(token),
  })
  await assertOk(response)
}

export async function recordEmployeeLogout(token: string): Promise<void> {
  const response = await fetch('/v1/session/logout', {
    method: 'POST',
    headers: authHeaders(token),
  })
  await assertOk(response)
}

export async function leaveMeeting(meetingId: string, token: string): Promise<void> {
  const response = await fetch(`/v1/meetings/${meetingId}/leave`, {
    method: 'POST',
    headers: authHeaders(token),
  })
  await assertOk(response)
}

export type MeetingInfo = {
  id: string
  title: string
  organizer_id: string
  locked: boolean
  ended: boolean
  pipeline_stage: string
  starts_at?: string | null
  ends_at?: string | null
  status?: MeetingStatus
}

export async function getMeeting(meetingId: string, token: string): Promise<MeetingInfo> {
  const response = await fetch(`/v1/meetings/${meetingId}`, {
    method: 'GET',
    headers: authHeaders(token),
  })
  await assertOk(response)
  return response.json() as Promise<MeetingInfo>
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
    headers: authHeaders(token, true),
    body: JSON.stringify({ device_id: livekitDeviceId() }),
  })

  await assertOk(response)
  return response.json() as Promise<LiveKitCredentials>
}

/** 每个浏览器标签一枚设备号，让同一员工能多端同时进 LiveKit 房。 */
export function livekitDeviceId(): string {
  const key = 'lkDeviceId'
  try {
    let id = sessionStorage.getItem(key)
    if (!id) {
      id = crypto.randomUUID().replace(/-/g, '').slice(0, 16)
      sessionStorage.setItem(key, id)
    }
    return id
  } catch {
    return ''
  }
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

export type CitedItem = {
  text: string
  source_segment_ids?: string[]
}

export type ActionItem = {
  task: string
  owner_user_id?: string
  deadline?: string | null
	source_segment_ids?: string[]
  source_message_ids?: string[]
  completed_at?: string
}

export type MeetingSummary = {
  meeting_id: string
  summary: string
  decisions: CitedItem[]
  action_items: ActionItem[]
  risks: CitedItem[]
  open_questions: CitedItem[]
  original_json?: string
  model?: string
  created_at: string
  revised_at?: string
}

export type SummaryRevision = {
  id: string
  meeting_id: string
  actor_key: string
  patch_json: string
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

/** 组织者/共同组织者触发私有 ASR 转写生成（会议须已结束）。 */
export async function generateTranscript(
  meetingId: string,
  token: string,
): Promise<{ meeting_id: string; pipeline_stage: string; segments: TranscriptSegment[] }> {
  const response = await fetch(`/v1/meetings/${meetingId}/transcript/generate`, {
    method: 'POST',
    headers: authHeaders(token),
  })
  await assertOk(response)
  return response.json() as Promise<{
    meeting_id: string
    pipeline_stage: string
    segments: TranscriptSegment[]
  }>
}

export async function retryPipeline(
  meetingId: string,
  token: string,
): Promise<{ pipeline_stage: string }> {
  const response = await fetch(`/v1/meetings/${meetingId}/pipeline/retry`, {
    method: 'POST',
    headers: authHeaders(token),
  })
  await assertOk(response)
  return response.json() as Promise<{ pipeline_stage: string }>
}

export async function listManualReview(token: string): Promise<EmployeeMeeting[]> {
  const response = await fetch('/v1/pipeline/manual-review', {
    method: 'GET',
    headers: authHeaders(token),
  })
  await assertOk(response)
  const payload = (await response.json()) as { meetings: EmployeeMeeting[] }
  return payload.meetings ?? []
}

export type DirectoryEmployee = {
  user_id: string
  display_name: string
}

export async function listDirectoryEmployees(
  token: string,
  query = '',
): Promise<DirectoryEmployee[]> {
  const qs = query.trim() ? `?q=${encodeURIComponent(query.trim())}` : ''
  const response = await fetch(`/v1/directory/employees${qs}`, {
    method: 'GET',
    headers: authHeaders(token),
  })
  await assertOk(response)
  const payload = (await response.json()) as { employees: DirectoryEmployee[] }
  return payload.employees ?? []
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

/** 组织者/共同组织者触发私有 LLM 纪要生成（会议须已结束）。 */
export async function generateSummary(
  meetingId: string,
  token: string,
): Promise<MeetingSummary | { accepted: true }> {
  const response = await fetch(`/v1/meetings/${meetingId}/summary/generate`, {
    method: 'POST',
    headers: authHeaders(token),
  })
  if (response.status === 202) {
    return { accepted: true }
  }
  await assertOk(response)
  return response.json() as Promise<MeetingSummary>
}

export async function patchSummary(
  meetingId: string,
  token: string,
  summary: Pick<MeetingSummary, 'summary' | 'decisions' | 'action_items' | 'risks' | 'open_questions'>,
): Promise<MeetingSummary> {
  const response = await fetch(`/v1/meetings/${meetingId}/summary`, {
    method: 'PATCH',
    headers: authHeaders(token, true),
    body: JSON.stringify(summary),
  })
  await assertOk(response)
  return response.json() as Promise<MeetingSummary>
}

export async function completeActionItem(
  meetingId: string,
  token: string,
  index: number,
): Promise<MeetingSummary> {
  const response = await fetch(
    `/v1/meetings/${meetingId}/summary/action-items/${index}/complete`,
    {
      method: 'POST',
      headers: authHeaders(token),
    },
  )
  await assertOk(response)
  return response.json() as Promise<MeetingSummary>
}

export type RetentionPolicy = {
  media_ttl_seconds: number
  video_ttl_seconds: number
  knowledge_ttl_seconds: number
  updated_at?: string
  updated_by?: string
}

export async function getRetentionPolicy(token: string): Promise<RetentionPolicy> {
  const response = await fetch('/v1/retention', {
    method: 'GET',
    headers: authHeaders(token),
  })
  await assertOk(response)
  return response.json() as Promise<RetentionPolicy>
}

export async function putRetentionPolicy(
  token: string,
  policy: Pick<RetentionPolicy, 'media_ttl_seconds' | 'video_ttl_seconds' | 'knowledge_ttl_seconds'>,
): Promise<RetentionPolicy> {
  const response = await fetch('/v1/retention', {
    method: 'PUT',
    headers: authHeaders(token, true),
    body: JSON.stringify(policy),
  })
  await assertOk(response)
  return response.json() as Promise<RetentionPolicy>
}

export async function getSummaryRevisions(
  meetingId: string,
  token: string,
): Promise<SummaryRevision[]> {
  const response = await fetch(`/v1/meetings/${meetingId}/summary/revisions`, {
    method: 'GET',
    headers: authHeaders(token),
  })
  await assertOk(response)
  const payload = (await response.json()) as { revisions: SummaryRevision[] }
  return payload.revisions ?? []
}

export type KnowledgeHit = {
  document: {
    meeting_id: string
    title: string
    text: string
    source_type: string
    source_id?: string
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

/** 显式下载产物并写入 artifact_download / artifact_export 审计。 */
export async function downloadArtifact(
  meetingId: string,
  token: string,
  kind: 'transcript' | 'summary' | 'media' | 'export',
  artifactId?: string,
): Promise<{
  kind: string
  audited: boolean
  artifacts?: MediaArtifact[]
  segments?: TranscriptSegment[]
  summary?: MeetingSummary
  has_summary?: boolean
}> {
  const response = await fetch(`/v1/meetings/${meetingId}/artifacts/download`, {
    method: 'POST',
    headers: authHeaders(token, true),
    body: JSON.stringify({ kind, artifact_id: artifactId ?? '' }),
  })
  await assertOk(response)
  return response.json() as Promise<{ kind: string; audited: boolean; artifacts?: MediaArtifact[] }>
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

export async function denyBreakGlass(
  meetingId: string,
  reqId: string,
  token: string,
): Promise<BreakGlassRequest> {
  const response = await fetch(`/v1/meetings/${meetingId}/break-glass/${reqId}/deny`, {
    method: 'POST',
    headers: authHeaders(token),
  })
  await assertOk(response)
  return response.json() as Promise<BreakGlassRequest>
}

export async function revokeBreakGlass(
  meetingId: string,
  reqId: string,
  token: string,
): Promise<BreakGlassRequest> {
  const response = await fetch(`/v1/meetings/${meetingId}/break-glass/${reqId}/revoke`, {
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

export type SharedReader = {
  email: string
  created_by: string
  created_at: string
  verified: boolean
}

export async function listSharedReaders(
  meetingId: string,
  token: string,
): Promise<SharedReader[]> {
  const response = await fetch(`/v1/meetings/${meetingId}/shared-readers`, {
    method: 'GET',
    headers: authHeaders(token),
  })
  await assertOk(response)
  const payload = (await response.json()) as { readers: SharedReader[] }
  return payload.readers ?? []
}

export async function addSharedReader(
  meetingId: string,
  token: string,
  email: string,
): Promise<{ readers: SharedReader[]; code?: string; magic_url?: string }> {
  const response = await fetch(`/v1/meetings/${meetingId}/shared-readers`, {
    method: 'POST',
    headers: authHeaders(token, true),
    body: JSON.stringify({ email }),
  })
  await assertOk(response)
  const payload = (await response.json()) as {
    readers: SharedReader[]
    code?: string
    magic_url?: string
  }
  return {
    readers: payload.readers ?? [],
    code: payload.code,
    magic_url: payload.magic_url,
  }
}

export async function removeSharedReader(
  meetingId: string,
  token: string,
  email: string,
): Promise<SharedReader[]> {
  const response = await fetch(
    `/v1/meetings/${meetingId}/shared-readers?email=${encodeURIComponent(email)}`,
    {
      method: 'DELETE',
      headers: authHeaders(token),
    },
  )
  await assertOk(response)
  const payload = (await response.json()) as { readers: SharedReader[] }
  return payload.readers ?? []
}

export async function requestSharedReaderVerification(
  meetingId: string,
  email: string,
): Promise<{ expires_at: string }> {
  const response = await fetch(`/v1/meetings/${meetingId}/shared-readers/verify`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...metuaiClientHeaders() },
    body: JSON.stringify({ email }),
  })
  await assertOk(response)
  return response.json() as Promise<{ expires_at: string }>
}

export async function confirmSharedReaderVerification(
  meetingId: string,
  email: string,
  code: string,
): Promise<{ access_token: string; email: string }> {
  const response = await fetch(`/v1/meetings/${meetingId}/shared-readers/confirm`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...metuaiClientHeaders() },
    body: JSON.stringify({ email, code }),
  })
  await assertOk(response)
  return response.json() as Promise<{ access_token: string; email: string }>
}

export type MediaArtifact = {
  id: string
  meeting_id: string
  kind: string
  status: string
  object_key: string
  detail: string
  egress_id: string
  participant_key?: string
  created_at?: string
  download_url?: string
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
