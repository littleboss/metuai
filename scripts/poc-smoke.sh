#!/usr/bin/env bash
# PoC 自动化验收：栈健康 → 建会 → 本机录音分块上传 → MinIO → 假流水线。
# 不覆盖「真人 WebRTC 开会 + Egress ready」（仍需手工或 egresscheck）。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GW="${GATEWAY_BASE_URL:-http://127.0.0.1:18080}"
COMPOSE="$ROOT/infra/compose"

echo "== 0) egress stack =="
bash "$ROOT/scripts/check-egress-stack.sh"

echo "== 1) gateway healthz + readyz =="
if ! curl -sf "$GW/healthz" >/tmp/metuai-healthz.json; then
  echo "FAIL: gateway not reachable at $GW"
  echo "hint: set -a; source infra/compose/.env.example; set +a; cd services/gateway && go run ./cmd/gateway"
  exit 1
fi
cat /tmp/metuai-healthz.json
echo
if ! curl -sf "$GW/readyz" >/tmp/metuai-readyz.json; then
  echo "FAIL: gateway not ready (GET /readyz). Ensure EMPLOYEE_JWT_SECRET, GUEST_JWT_SECRET, DATABASE_URL are set."
  cat /tmp/metuai-readyz.json 2>/dev/null || true
  exit 1
fi
cat /tmp/metuai-readyz.json
echo

echo "== 2) employee token =="
# 必须与网关共享显式 EMPLOYEE_JWT_SECRET（无代码内默认值）。
if [[ -z "${EMPLOYEE_JWT_SECRET:-}" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "$COMPOSE/.env.example"
  set +a
fi
TOKEN="$(cd "$ROOT/services/gateway" && go run ./cmd/devtoken)"
AUTH="Authorization: Bearer $TOKEN"

echo "== 3) create meeting =="
CREATE="$(curl -sf -X POST "$GW/v1/meetings" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"title":"poc-smoke"}')"
MEETING_ID="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$CREATE")"
echo "meeting_id=$MEETING_ID"

echo "== 4) recording ack =="
curl -sf -X POST "$GW/v1/meetings/$MEETING_ID/recording-ack" -H "$AUTH" \
  -H 'Content-Type: application/json' -d '{"acknowledged":true}' >/dev/null

UPLOAD_ID="smoke_$(date +%s)"
PART_BODY="hello-metuai-poc-smoke"
# macOS: shasum; Linux: sha256sum
if command -v shasum >/dev/null; then
  PART_SUM="$(printf '%s' "$PART_BODY" | shasum -a 256 | awk '{print $1}')"
else
  PART_SUM="$(printf '%s' "$PART_BODY" | sha256sum | awk '{print $1}')"
fi

echo "== 5) put chunk + status =="
curl -sf -X PUT "$GW/v1/meetings/$MEETING_ID/local-recording/$UPLOAD_ID/chunks/0" \
  -H "$AUTH" -H "X-Checksum-Sha256: $PART_SUM" \
  --data-binary "$PART_BODY" >/dev/null
STATUS="$(curl -sf "$GW/v1/meetings/$MEETING_ID/local-recording/$UPLOAD_ID/status" -H "$AUTH")"
echo "$STATUS"
echo "$STATUS" | grep -q '"received":\[0\]' || echo "$STATUS" | grep -q '"received":[0]' || {
  # JSON may be "received":[0] without spaces
  echo "$STATUS" | python3 -c 'import json,sys; s=json.load(sys.stdin); assert s.get("received")==[0], s'
}

echo "== 6) complete → MinIO =="
COMPLETE="$(curl -sf -X POST "$GW/v1/meetings/$MEETING_ID/local-recording/$UPLOAD_ID/complete" \
  -H "$AUTH" -H 'Content-Type: application/json' -d '{"parts":1}')"
echo "$COMPLETE"
STORED="$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("stored_in",""))' <<<"$COMPLETE")"
OBJ="$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("object_key",""))' <<<"$COMPLETE")"
if [[ "$STORED" != "s3" && "$STORED" != "local_spool" && "$STORED" != "local_spool_only" ]]; then
  echo "FAIL: unexpected stored_in=$STORED"
  exit 1
fi
if [[ "$STORED" == "s3" ]]; then
  KEY_PREFIX="local-recording/$MEETING_ID/"
  docker exec compose-minio-1 /usr/bin/mc alias set local http://127.0.0.1:9000 metuai metuai-secret >/dev/null
  docker exec compose-minio-1 /usr/bin/mc ls "local/metuai-media/$KEY_PREFIX" | head -5
  echo "MinIO OK under $KEY_PREFIX"
else
  echo "WARN: stored_in=$STORED (S3 upload not ready; spool path still valid for PoC)"
fi

echo "== 7) end meeting + fake pipeline =="
curl -sf -X POST "$GW/v1/meetings/$MEETING_ID/end" -H "$AUTH" >/dev/null
curl -sf -X POST "$GW/v1/meetings/$MEETING_ID/pipeline/run-fake" -H "$AUTH" >/dev/null
PIPE="$(curl -sf "$GW/v1/meetings/$MEETING_ID/pipeline" -H "$AUTH")"
echo "$PIPE"
echo "$PIPE" | python3 -c 'import json,sys; s=json.load(sys.stdin); assert s.get("pipeline_stage")=="READY", s'

echo "== 8) media + audit =="
MEDIA="$(curl -sf "$GW/v1/meetings/$MEETING_ID/media" -H "$AUTH")"
echo "$MEDIA" | python3 -m json.tool | head -40
AUDITS="$(curl -sf "$GW/v1/meetings/$MEETING_ID/audit" -H "$AUTH")"
echo "$AUDITS" | python3 -c '
import json,sys
events=json.load(sys.stdin).get("events") or []
actions={e.get("action") for e in events}
need={"local_recording_uploaded","meeting_created"}
missing=need-actions
assert not missing, (missing, sorted(actions))
print("audit ok:", sorted(a for a in actions if a.startswith(("local_","meeting_"))))
'

echo
echo "OK: poc-smoke passed for meeting $MEETING_ID (object_key=$OBJ stored_in=$STORED)"
echo "After-meeting UI: open http://127.0.0.1:5173/meeting/$MEETING_ID"
echo "Still manual: real WebRTC room + EGRESS_ENABLED room_audio ready"
