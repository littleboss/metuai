#!/usr/bin/env bash
# 检查 metuai compose 里 Egress 相关容器是否就绪（不启动会议）。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT/infra/compose"

echo "== compose ps =="
docker compose ps

need=(compose-redis-1 compose-livekit-1 compose-livekit-egress-1 compose-minio-1)
for name in "${need[@]}"; do
  if ! docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null | grep -q true; then
    echo "FAIL: $name is not running"
    echo "hint: docker compose -f infra/compose/docker-compose.yml up -d"
    exit 1
  fi
done

echo "== livekit redis =="
docker logs compose-livekit-1 2>&1 | grep -E 'connecting to redis|starting LiveKit' | tail -3

echo "== egress ready =="
docker logs compose-livekit-egress-1 2>&1 | grep -E 'service ready|connecting to redis' | tail -5

echo "== minio bucket =="
docker exec compose-minio-1 /usr/bin/mc alias set local http://127.0.0.1:9000 metuai metuai-secret >/dev/null
docker exec compose-minio-1 /usr/bin/mc ls local/metuai-media/ | head -5 || true

echo "OK: stack looks ready."
echo "Next: EGRESS_ENABLED=true S3_ENDPOINT=http://minio:9000 go run ./cmd/egresscheck  (from services/gateway)"
