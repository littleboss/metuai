import {
  LiveKitRoom,
  RoomAudioRenderer,
  useLocalParticipant,
  useParticipants,
  useTracks,
  VideoTrack,
} from '@livekit/components-react'
import '@livekit/components-styles'
import { Track } from 'livekit-client'
import { useEffect, useMemo, useState } from 'react'
import { Button } from '../aura/Button'
import { ControlBar } from '../aura/ControlBar'
import { VideoTile } from '../aura/VideoTile'
import { endMeeting, heartbeat, leaveMeeting } from '../lib/api'
import {
  classifyJoinError,
  joinErrorBanner,
  type JoinErrorKind,
} from '../lib/livekitErrors'
import {
  canUseLocalRecording,
  stopAndUploadLocalRecording,
} from '../lib/localRecording'

type MeetingStageProps = {
  url: string
  token: string
  meetingId: string
  authToken: string
  isOrganizer: boolean
}

function StageGrid({
  meetingId,
  authToken,
  isOrganizer,
}: {
  meetingId: string
  authToken: string
  isOrganizer: boolean
}) {
  const { localParticipant, isMicrophoneEnabled, isCameraEnabled } = useLocalParticipant()
  const participants = useParticipants()
  const cameraTracks = useTracks([Track.Source.Camera], { onlySubscribed: false })
  const [ending, setEnding] = useState(false)

  useEffect(() => {
    const id = window.setInterval(() => {
      void heartbeat(meetingId, authToken).catch(() => undefined)
    }, 20_000)
    return () => window.clearInterval(id)
  }, [meetingId, authToken])

  const tiles = useMemo(() => {
    return participants.map((p) => {
      const cam = cameraTracks.find((t) => t.participant.identity === p.identity)
      const muted = p.isMicrophoneEnabled === false
      const camOff = !p.isCameraEnabled
      let state: 'streaming' | 'muted' | 'cam-off' | 'speaking' = 'streaming'
      if (p.isSpeaking) state = 'speaking'
      else if (camOff) state = 'cam-off'
      else if (muted) state = 'muted'
      return { participant: p, cam, state, name: p.name || p.identity }
    })
  }, [participants, cameraTracks])

  async function handleLeave() {
    try {
      if (canUseLocalRecording()) {
        await stopAndUploadLocalRecording().catch(() => undefined)
      }
      await leaveMeeting(meetingId, authToken)
    } finally {
      sessionStorage.removeItem('lkToken')
      window.location.assign(isOrganizer ? `/meeting/${encodeURIComponent(meetingId)}` : '/')
    }
  }

  async function handleEnd() {
    setEnding(true)
    try {
      if (canUseLocalRecording()) {
        await stopAndUploadLocalRecording().catch(() => undefined)
      }
      await endMeeting(meetingId, authToken)
      window.location.assign(`/meeting/${encodeURIComponent(meetingId)}`)
    } catch {
      setEnding(false)
    }
  }

  return (
    <div className="relative flex min-h-[calc(100vh-3.5rem)] flex-col bg-bg-app pb-24">
      <div className="grid flex-1 grid-cols-1 gap-2 p-2 sm:grid-cols-2 lg:grid-cols-3">
        {tiles.map(({ participant, cam, state, name }) => (
          <VideoTile key={participant.identity} name={name} state={state}>
            {cam?.publication?.track ? <VideoTrack trackRef={cam} className="h-full w-full object-cover" /> : null}
          </VideoTile>
        ))}
      </div>
      <RoomAudioRenderer />
      <ControlBar
        micOn={isMicrophoneEnabled}
        camOn={isCameraEnabled}
        onToggleMic={() => void localParticipant.setMicrophoneEnabled(!isMicrophoneEnabled)}
        onToggleCam={() => void localParticipant.setCameraEnabled(!isCameraEnabled)}
        onShare={() => void localParticipant.setScreenShareEnabled(true)}
        onLeave={() => void handleLeave()}
        endMeeting={
          isOrganizer ? (
            <Button variant="danger" loading={ending} onClick={() => void handleEnd()} aria-label="结束会议">
              结束会议
            </Button>
          ) : null
        }
      />
    </div>
  )
}

/** 会中网格舞台：LiveKit + Aura ControlBar / VideoTile。cam/mic 拒绝仍可入会。 */
export function MeetingStage(props: MeetingStageProps) {
  const [joinError, setJoinError] = useState<{ kind: JoinErrorKind; detail: string } | null>(null)

  return (
    <div className="min-h-screen bg-bg-app text-text">
      <header className="flex items-center justify-between border-b border-border px-4 py-3">
        <span className="text-lg font-semibold tracking-tight">METUAI</span>
        <span className="font-mono text-xs tracking-[0.2em] text-secondary">{props.meetingId}</span>
      </header>
      {joinError ? (
        <p className="px-4 py-2 text-sm text-secondary">
          {joinErrorBanner(joinError.kind, joinError.detail)}
        </p>
      ) : null}
      <LiveKitRoom
        serverUrl={props.url}
        token={props.token}
        connect
        video
        audio
        onError={(e) => setJoinError({ kind: classifyJoinError(e.message), detail: e.message })}
        onMediaDeviceFailure={() =>
          setJoinError({ kind: 'permission', detail: 'permission-denied' })
        }
        className="min-h-[calc(100vh-3.5rem)]"
      >
        <StageGrid
          meetingId={props.meetingId}
          authToken={props.authToken}
          isOrganizer={props.isOrganizer}
        />
      </LiveKitRoom>
    </div>
  )
}
