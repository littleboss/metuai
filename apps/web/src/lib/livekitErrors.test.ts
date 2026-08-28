import assert from 'node:assert/strict'
import { classifyJoinError, isPermissionBannerCopy, joinErrorBanner } from './livekitErrors'

function testSignalErrorsDoNotUsePermissionCopy() {
  const detail = 'could not establish signal connection: Failed to fetch'
  const kind = classifyJoinError(detail)
  assert.equal(kind, 'signal')
  const banner = joinErrorBanner(kind, detail)
  assert.ok(!isPermissionBannerCopy(banner), `signal banner must not mention 设备权限受限: ${banner}`)
  assert.ok(banner.includes('连不上媒体服务'))
}

function testPermissionErrorsKeepPermissionCopy() {
  const detail = 'permission-denied'
  const kind = classifyJoinError(detail)
  assert.equal(kind, 'permission')
  const banner = joinErrorBanner(kind, detail)
  assert.ok(isPermissionBannerCopy(banner))
}

testSignalErrorsDoNotUsePermissionCopy()
testPermissionErrorsKeepPermissionCopy()

console.log('livekitErrors contract tests passed')
