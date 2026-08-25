export type CreatedMeeting = {
  id: string
  title: string
  password: string
  organizer_id: string
}

type GuestSession = {
  token: string
}

export type LiveKitCredentials = {
  token: string
  livekit_url: string
}

// 将网关返回的正文保留在错误中，便于页面展示具体失败原因。
async function assertOk(response: Response): Promise<void> {
  if (!response.ok) {
    const message = await response.text()
    throw new Error(message || `请求失败（HTTP ${response.status}）`)
  }
}

export async function createMeeting(
  employeeToken: string,
  title: string,
): Promise<CreatedMeeting> {
  const response = await fetch('/v1/meetings', {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${employeeToken}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ title }),
  })

  await assertOk(response)
  return response.json() as Promise<CreatedMeeting>
}

export async function guestSession(
  meetingId: string,
  password: string,
  displayName: string,
): Promise<GuestSession> {
  const response = await fetch(`/v1/meetings/${meetingId}/guest-session`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password, display_name: displayName }),
  })

  await assertOk(response)
  return response.json() as Promise<GuestSession>
}

export async function ackRecording(
  meetingId: string,
  token: string,
): Promise<void> {
  const response = await fetch(`/v1/meetings/${meetingId}/recording-ack`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },
  })

  await assertOk(response)
}

export async function livekitToken(
  meetingId: string,
  token: string,
): Promise<LiveKitCredentials> {
  const response = await fetch(`/v1/meetings/${meetingId}/livekit-token`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },
  })

  await assertOk(response)
  return response.json() as Promise<LiveKitCredentials>
}
