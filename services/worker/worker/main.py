"""会后 Worker：假流水线，或 stub/FunASR 转写后提交网关。

用法::

    export GATEWAY_URL=http://127.0.0.1:18080
    export EMPLOYEE_JWT="$(cd ../../services/gateway && go run ./cmd/devtoken)"

    # 假流水线（整条到 READY）
    python -m worker.main --mode fake --meeting mtg_xxx --once

    # ASR stub（不装 funasr；写到 TRANSCRIPT_READY）
    python -m worker.main --mode asr --meeting mtg_xxx --once

    # 真 FunASR（需 pip install funasr + 模型）
    ASR_BACKEND=funasr FUNASR_MODEL=paraformer-zh \\
      python -m worker.main --mode asr --meeting mtg_xxx --audio /path/to.wav --once
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import sys
import tempfile
import urllib.error
import urllib.request
from enum import Enum
from pathlib import Path

from worker.asr import Segment, transcribe_audio

log = logging.getLogger("metuai.worker")


class PipelineStage(str, Enum):
    RECORDING_FINALIZED = "RECORDING_FINALIZED"
    MEDIA_READY = "MEDIA_READY"
    TRANSCRIBING = "TRANSCRIBING"
    TRANSCRIPT_READY = "TRANSCRIPT_READY"
    EXTRACTING_ARTIFACTS = "EXTRACTING_ARTIFACTS"
    INDEXING = "INDEXING"
    READY = "READY"
    RETRYABLE_ERROR = "RETRYABLE_ERROR"
    MANUAL_REVIEW = "MANUAL_REVIEW"


def _request(method: str, url: str, token: str, body: dict | None = None) -> tuple[int, dict]:
    data = None
    headers = {"Authorization": f"Bearer {token}"}
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            raw = resp.read().decode("utf-8") or "{}"
            return resp.status, json.loads(raw)
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode("utf-8") or "{}"
        try:
            payload = json.loads(raw)
        except json.JSONDecodeError:
            payload = {"error": raw}
        return exc.code, payload


def run_fake_for_meeting(gateway: str, token: str, meeting_id: str) -> int:
    status, payload = _request(
        "POST",
        f"{gateway}/v1/meetings/{meeting_id}/pipeline/run-fake",
        token,
    )
    print(f"fake meeting={meeting_id} http={status} body={payload}")
    return 0 if status == 200 else 1


def _pick_media_source(artifacts: list[dict]) -> tuple[str, str]:
    """优先 Egress 独立音轨，仅在缺轨时使用员工本机备份。"""
    by_kind = {a.get("kind"): a for a in artifacts if a.get("status") == "ready"}
    for kind in ("participant_track", "local_mic"):
        art = by_kind.get(kind)
        if art and art.get("object_key"):
            return kind, art["object_key"]
    return "", ""


def _try_fetch_minio(object_key: str, dest: Path) -> bool:
    """可选：用 boto3 从 MinIO 拉对象。未装 boto3 或失败则返回 False。"""
    try:
        import boto3  # type: ignore
        from botocore.client import Config  # type: ignore
    except ImportError:
        log.info("boto3 not installed; skip MinIO download")
        return False

    endpoint = os.getenv("S3_UPLOAD_ENDPOINT") or os.getenv("S3_ENDPOINT") or "http://127.0.0.1:19000"
    bucket = os.getenv("S3_BUCKET", "metuai-media")
    key = object_key
    prefix = bucket + "/"
    if key.startswith(prefix):
        key = key[len(prefix) :]
    try:
        client = boto3.client(
            "s3",
            endpoint_url=endpoint,
            aws_access_key_id=os.getenv("S3_ACCESS_KEY", "metuai"),
            aws_secret_access_key=os.getenv("S3_SECRET_KEY", "metuai-secret"),
            region_name=os.getenv("S3_REGION", "us-east-1"),
            config=Config(s3={"addressing_style": "path"}),
        )
        client.download_file(bucket, key, str(dest))
        log.info("downloaded s3://%s/%s -> %s", bucket, key, dest)
        return dest.is_file() and dest.stat().st_size > 0
    except Exception as exc:  # noqa: BLE001 — PoC：拉失败就走 stub 无文件路径
        log.warning("MinIO download failed: %s", exc)
        return False


def run_asr_for_meeting(
    gateway: str,
    token: str,
    meeting_id: str,
    *,
    audio: str | None,
    backend: str | None,
) -> int:
    status, pipe = _request("GET", f"{gateway}/v1/meetings/{meeting_id}/pipeline", token)
    if status != 200:
        print(f"pipeline http={status} body={pipe}", file=sys.stderr)
        return 1
    if not pipe.get("ended"):
        print("meeting_not_ended: end the meeting before ASR", file=sys.stderr)
        return 1
    title = pipe.get("title") or ""

    status, media = _request("GET", f"{gateway}/v1/meetings/{meeting_id}/media", token)
    artifacts = media.get("artifacts") or [] if status == 200 else []
    kind, object_key = _pick_media_source(artifacts)
    source = "local_fallback" if kind == "local_mic" else "egress"

    if audio is None and not object_key:
        print("authoritative_audio_not_ready: participant track missing and no local fallback", file=sys.stderr)
        return 1

    audio_path: Path | None = Path(audio) if audio else None
    tmp: tempfile.TemporaryDirectory[str] | None = None
    if audio_path is None and object_key:
        tmp = tempfile.TemporaryDirectory(prefix="metuai-asr-")
        dest = Path(tmp.name) / "audio.bin"
        if _try_fetch_minio(object_key, dest):
            audio_path = dest
        else:
            log.warning("no local audio; stub will run without file (object_key=%s)", object_key)

    try:
        used, segments = transcribe_audio(
            audio_path,
            backend=backend,
            source=source if source else "egress",
            meeting_title=title,
        )
    except Exception as exc:  # noqa: BLE001
        print(f"asr failed: {exc}", file=sys.stderr)
        return 1
    finally:
        if tmp is not None:
            tmp.cleanup()

    body = {
        "backend": used,
        "segments": [s.to_api() if isinstance(s, Segment) else s for s in segments],
    }
    status, payload = _request(
        "POST",
        f"{gateway}/v1/meetings/{meeting_id}/pipeline/asr-result",
        token,
        body,
    )
    print(f"asr meeting={meeting_id} backend={used} http={status} body={payload}")
    return 0 if status == 200 else 1


def main(argv: list[str] | None = None) -> int:
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(name)s: %(message)s")
    parser = argparse.ArgumentParser(description="Metuai post-meeting worker")
    parser.add_argument("--gateway", default=os.getenv("GATEWAY_URL", "http://127.0.0.1:18080"))
    parser.add_argument("--token", default=os.getenv("EMPLOYEE_JWT", ""))
    parser.add_argument("--meeting", default=os.getenv("MEETING_ID", ""))
    parser.add_argument("--mode", choices=("fake", "asr"), default=os.getenv("WORKER_MODE", "fake"))
    parser.add_argument("--audio", default=os.getenv("ASR_AUDIO", ""), help="本地音频路径（asr 模式）")
    parser.add_argument("--backend", default=os.getenv("ASR_BACKEND", ""), help="stub|funasr")
    parser.add_argument("--once", action="store_true", help="处理一场会后退出（PoC 默认行为）")
    args = parser.parse_args(argv)

    if not args.token:
        print("EMPLOYEE_JWT / --token required", file=sys.stderr)
        return 2
    if not args.meeting:
        print("MEETING_ID / --meeting required", file=sys.stderr)
        return 2

    gateway = args.gateway.rstrip("/")
    if args.mode == "fake":
        return run_fake_for_meeting(gateway, args.token, args.meeting)
    return run_asr_for_meeting(
        gateway,
        args.token,
        args.meeting,
        audio=args.audio or None,
        backend=args.backend or None,
    )


if __name__ == "__main__":
    raise SystemExit(main())
