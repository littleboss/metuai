import { useMemo, useState } from 'react'
import type { AuthUser, CreatedMeeting } from './lib/api'
import { AuthPage } from './pages/AuthPage'
import { Error401, Error403, Error503 } from './pages/ErrorPages'
import { JoinGatePage } from './pages/JoinGatePage'
import { LobbyPage } from './pages/LobbyPage'
import { MeetingListPage } from './pages/MeetingListPage'
import { MeetingStage } from './pages/MeetingStage'
import { NotesPage } from './pages/NotesPage'

function usePath() {
  return window.location.pathname
}

function readStoredUser(): AuthUser | null {
  try {
    const raw = sessionStorage.getItem('employeeUser')
    if (!raw) return null
    return JSON.parse(raw) as AuthUser
  } catch {
    return null
  }
}

function App() {
  const path = usePath()
  const [employeeToken, setEmployeeToken] = useState(
    () => sessionStorage.getItem('employeeToken') ?? '',
  )
  const [employeeUser, setEmployeeUser] = useState<AuthUser | null>(() => readStoredUser())
  const [created, setCreated] = useState<CreatedMeeting | null>(null)

  const guestMatch = path.match(/^\/join\/([^/]+)\/?$/)
  const employeeJoinMatch = path.match(/^\/employee-join\/([^/]+)\/?$/)
  const lobbyMatch = path.match(/^\/lobby\/([^/]+)\/?$/)
  const afterMatch = path.match(/^\/meeting\/([^/]+)\/?$/)

  const readyMissing = useMemo(() => {
    const params = new URLSearchParams(window.location.search)
    const raw = params.get('missing')
    return raw ? raw.split(',').filter(Boolean) : undefined
  }, [])

  function handleAuthenticated(token: string, user: AuthUser) {
    setEmployeeToken(token)
    setEmployeeUser(user)
  }

  if (path === '/error/401') return <Error401 />
  if (path === '/error/403') return <Error403 />
  if (path === '/error/503') return <Error503 missing={readyMissing} />

  if (guestMatch) {
    return <JoinGatePage meetingId={decodeURIComponent(guestMatch[1])} mode="guest" />
  }
  if (employeeJoinMatch) {
    return <JoinGatePage meetingId={decodeURIComponent(employeeJoinMatch[1])} mode="employee" />
  }

  if (path === '/room' || path === '/room/') {
    const token = sessionStorage.getItem('lkToken')
    const url = sessionStorage.getItem('lkUrl')
    const meetingId = sessionStorage.getItem('meetingId')
    const authToken = sessionStorage.getItem('authToken')
    const isOrganizer = sessionStorage.getItem('isOrganizer') === '1'
    if (token && url && meetingId && authToken) {
      return (
        <MeetingStage
          token={token}
          url={url}
          meetingId={meetingId}
          authToken={authToken}
          isOrganizer={isOrganizer}
        />
      )
    }
    return <Error401 message="入会凭证不存在或已过期，请重新进入。" />
  }

  if (afterMatch) {
    const meetingId = decodeURIComponent(afterMatch[1])
    const auth =
      sessionStorage.getItem('authToken') ||
      sessionStorage.getItem('employeeToken') ||
      ''
    if (!auth) return <Error401 message="查看纪要需要有效会话。" />
    return <NotesPage meetingId={meetingId} authToken={auth} />
  }

  if (lobbyMatch) {
    const meetingId = decodeURIComponent(lobbyMatch[1])
    if (!employeeToken) {
      return <AuthPage onAuthenticated={handleAuthenticated} />
    }
    return (
      <LobbyPage
        meetingId={meetingId}
        employeeToken={employeeToken}
        initial={created?.id === meetingId ? created : null}
      />
    )
  }

  if (!employeeToken) {
    return <AuthPage onAuthenticated={handleAuthenticated} />
  }

  return (
    <MeetingListPage
      employeeToken={employeeToken}
      displayName={employeeUser?.display_name}
      onLogout={() => {
        sessionStorage.removeItem('employeeToken')
        sessionStorage.removeItem('employeeUser')
        setEmployeeToken('')
        setEmployeeUser(null)
      }}
      onCreated={(meeting) => {
        setCreated(meeting)
        sessionStorage.setItem('roomPassword', meeting.password)
        sessionStorage.setItem('meetingId', meeting.id)
        window.location.assign(`/lobby/${encodeURIComponent(meeting.id)}`)
      }}
      onOpenMeeting={(id) => {
        window.location.assign(`/meeting/${encodeURIComponent(id)}`)
      }}
    />
  )
}

export default App
