import { LiveKitRoom, VideoConference } from '@livekit/components-react'
import '@livekit/components-styles'

type RoomPageProps = {
  url: string
  token: string
}

export function RoomPage({ url, token }: RoomPageProps) {
  return (
    <main className="room-page" data-lk-theme="default">
      <LiveKitRoom
        serverUrl={url}
        token={token}
        connect
        audio
        video
        onDisconnected={() => window.location.assign('/')}
      >
        {/* LiveKit 提供设备选择、参与者画面、聊天和离会控制。 */}
        <VideoConference />
      </LiveKitRoom>
    </main>
  )
}
