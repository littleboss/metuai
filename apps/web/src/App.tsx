import { AfterMeetingPage } from './pages/AfterMeetingPage'
import { HomePage } from './pages/HomePage'
import { JoinEmployeePage } from './pages/JoinEmployeePage'
import { JoinGuestPage } from './pages/JoinGuestPage'
import { RoomPage } from './pages/RoomPage'

function App() {
  const path = window.location.pathname
  const guestMatch = path.match(/^\/join\/([^/]+)\/?$/)
  const employeeJoinMatch = path.match(/^\/employee-join\/([^/]+)\/?$/)
  const afterMatch = path.match(/^\/meeting\/([^/]+)\/?$/)

  if (guestMatch) {
    return <JoinGuestPage meetingId={decodeURIComponent(guestMatch[1])} />
  }

  if (employeeJoinMatch) {
    return <JoinEmployeePage meetingId={decodeURIComponent(employeeJoinMatch[1])} />
  }

  if (afterMatch) {
    return <AfterMeetingPage meetingId={decodeURIComponent(afterMatch[1])} />
  }

  if (path === '/room' || path === '/room/') {
    const token = sessionStorage.getItem('lkToken')
    const url = sessionStorage.getItem('lkUrl')
    const meetingId = sessionStorage.getItem('meetingId')
    const authToken = sessionStorage.getItem('authToken')
    const isOrganizer = sessionStorage.getItem('isOrganizer') === '1'
    const initialPassword = sessionStorage.getItem('roomPassword') ?? ''

    if (token && url && meetingId && authToken) {
      return (
        <RoomPage
          token={token}
          url={url}
          meetingId={meetingId}
          authToken={authToken}
          isOrganizer={isOrganizer}
          initialPassword={initialPassword}
        />
      )
    }

    return (
      <main className="empty-state">
        <p className="eyebrow">ROOM CREDENTIALS MISSING</p>
        <h1>入会凭证不存在或已过期。</h1>
        <p>请从创建会议页或嘉宾邀请链接重新进入。</p>
        <a className="primary-button" href="/">
          返回首页 <span>→</span>
        </a>
      </main>
    )
  }

  return <HomePage />
}

export default App
