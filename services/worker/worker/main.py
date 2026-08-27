"""会后 Worker：假流水线，或 stub/FunASR 转写后提交网关。

用法::

    export GATEWAY_URL=http://127.0.0.1:18080
    # 通过 POST /v1/auth/login 获取员工 JWT
    export EMPLOYEE_JWT=...

    # 假流水线（整条到 READY）
    python -m worker.main --mode fake --meeting mtg_xxx --once

    # 从任务表领取一条（会议结束后网关会入队）
    python -m worker.main --claim --once

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


def claim_and_run(
    gateway: str,
    token: str,
    owner: str,
    mode: str,
    audio: str | None,
    backend: str | None,
) -> int:
    """领取一条会后任务：成功则 complete，失败则 fail（超限由网关标死信）。"""
    status, payload = _request(
        "POST",
        f"{gateway}/v1/pipeline/tasks/claim",
        token,
        {"owner": owner, "limit": 1, "kind": mode},
    )
    if status != 200:
        print(f"claim http={status} body={payload}", file=sys.stderr)
        return 1
    tasks = payload.get("tasks") or []
    if not tasks:
        print("claim: no tasks")
        return 0
    task = tasks[0]
    task_id = task.get("id") or ""
    meeting_id = task.get("meeting_id") or ""
    kind = task.get("kind") or mode
    print(f"claimed id={task_id} meeting={meeting_id} kind={kind}")
    try:
        if kind == "asr":
            rc = run_asr_for_meeting(gateway, token, meeting_id, audio=audio, backend=backend)
        else:
            rc = run_fake_for_meeting(gateway, token, meeting_id)
    except Exception as exc:  # pragma: no cover - 保护租约不被卡死
        log.exception("task %s crashed", task_id)
        _request("POST", f"{gateway}/v1/pipeline/tasks/{task_id}/fail", token, {"error": str(exc)})
        return 1
    if rc == 0:
        done_status, done_body = _request("POST", f"{gateway}/v1/pipeline/tasks/{task_id}/complete", token)
        print(f"complete http={done_status} body={done_body}")
        return 0 if done_status == 200 else 1
    fail_status, fail_body = _request(
        "POST",
        f"{gateway}/v1/pipeline/tasks/{task_id}/fail",
        token,
        {"error": f"worker exit {rc}"},
    )
    print(f"fail http={fail_status} body={fail_body}")
    return rc


def _iter_audio_sources(artifacts: list[dict]) -> list[dict]:
    """逐参会人选择权威音源：独立音轨优先，缺轨再用该人的本机备份。房间混音永不入选。"""
    ready = [a for a in artifacts if a.get("status") == "ready" and a.get("object_key")]
    tracks = [a for a in ready if a.get("kind") == "participant_track"]
    local = [a for a in ready if a.get("kind") == "local_mic"]
    covered: set[str] = set()
    out: list[dict] = []
    for art in tracks:
        key = (art.get("participant_key") or art.get("object_key") or "").strip()
        out.append({"kind": "participant_track", "object_key": art["object_key"], "participant_key": art.get("participant_key") or ""})
        if key:
            covered.add(key)
    for art in local:
        key = (art.get("participant_key") or "").strip()
        if key and key in covered:
            continue
        out.append({"kind": "local_mic", "object_key": art["object_key"], "participant_key": key})
        if key:
            covered.add(key)
    return out


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


def _speaker_fields(participant_key: str) -> tuple[str, str]:
    key = (participant_key or "").strip()
    if key.startswith("employee:") or key.startswith("guest:"):
        uid = key.split(":", 1)[1]
        return uid, uid
    if key:
        return key, key
    return "speaker", "说话人"


def mark_manual_review(gateway: str, token: str, meeting_id: str, reason: str) -> int:
    status, payload = _request(
        "POST",
        f"{gateway}/v1/meetings/{meeting_id}/pipeline/manual-review",
        token,
        {"reason": reason},
    )
    print(f"manual-review meeting={meeting_id} http={status} body={payload}")
    return 0 if status == 200 else 1


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
    sources = _iter_audio_sources(artifacts)

    if audio is None and not sources:
        return mark_manual_review(
            gateway,
            token,
            meeting_id,
            "no participant_track or local_mic; room mix is not an ASR source",
        )

    collected: list[Segment] = []
    used = "stub"
    tmp: tempfile.TemporaryDirectory[str] | None = None

    def transcribe_one(source: str, participant_key: str, audio_path: Path | None) -> int:
        nonlocal used, collected
        try:
            used, segments = transcribe_audio(
                audio_path,
                backend=backend,
                source=source,
                meeting_title=title,
            )
        except Exception as exc:  # noqa: BLE001
            print(f"asr failed: {exc}", file=sys.stderr)
            return 1
        uid, display = _speaker_fields(participant_key)
        for i, seg in enumerate(segments):
            if participant_key:
                seg.speaker_user_id = uid
                if not seg.speaker_display_name or seg.speaker_display_name.startswith("说话人"):
                    seg.speaker_display_name = display
                if not seg.track_id or seg.track_id.startswith(("stub-", "funasr-")):
                    seg.track_id = f"{participant_key}-{i}"
            collected.append(seg)
        return 0

    try:
        if audio:
            if transcribe_one("egress", "", Path(audio)) != 0:
                return 1
        else:
            for src in sources:
                source = "local_fallback" if src["kind"] == "local_mic" else "egress"
                audio_path: Path | None = None
                if tmp is None:
                    tmp = tempfile.TemporaryDirectory(prefix="metuai-asr-")
                dest = Path(tmp.name) / f"{src.get('participant_key') or src['kind']}-{len(collected)}.bin"
                dest = Path(str(dest).replace(":", "_"))
                if _try_fetch_minio(src["object_key"], dest):
                    audio_path = dest
                else:
                    log.warning("no local audio; stub will run without file (object_key=%s)", src["object_key"])
                if transcribe_one(source, src.get("participant_key") or "", audio_path) != 0:
                    return 1
    finally:
        if tmp is not None:
            tmp.cleanup()

    if not collected:
        return mark_manual_review(gateway, token, meeting_id, "asr produced no segments")

    body = {
        "backend": used,
        "segments": [s.to_api() if isinstance(s, Segment) else s for s in collected],
    }
    status, payload = _request(
        "POST",
        f"{gateway}/v1/meetings/{meeting_id}/pipeline/asr-result",
        token,
        body,
    )
    print(f"asr meeting={meeting_id} backend={used} segments={len(collected)} http={status} body={payload}")
    return 0 if status == 200 else 1


def main(argv: list[str] | None = None) -> int:
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(name)s: %(message)s")
    parser = argparse.ArgumentParser(description="Metuai post-meeting worker")
    parser.add_argument("--gateway", default=os.getenv("GATEWAY_URL", "http://127.0.0.1:18080"))
    parser.add_argument(
        "--token",
        default=os.getenv("WORKER_TOKEN") or os.getenv("EMPLOYEE_JWT", ""),
    )
    parser.add_argument("--meeting", default=os.getenv("MEETING_ID", ""))
    parser.add_argument("--mode", choices=("fake", "asr"), default=os.getenv("WORKER_MODE", "fake"))
    parser.add_argument("--audio", default=os.getenv("ASR_AUDIO", ""), help="本地音频路径（asr 模式）")
    parser.add_argument("--backend", default=os.getenv("ASR_BACKEND", ""), help="stub|funasr")
    parser.add_argument("--once", action="store_true", help="处理一场会后退出（PoC 默认行为）")
    parser.add_argument("--claim", action="store_true", help="从网关任务表领取一条作业（可与 --once 同用）")
    parser.add_argument("--owner", default=os.getenv("WORKER_OWNER", "worker"), help="租约持有者名")
    args = parser.parse_args(argv)

    if not args.token:
        print("WORKER_TOKEN / EMPLOYEE_JWT / --token required", file=sys.stderr)
        return 2
    gateway = args.gateway.rstrip("/")
    if args.claim:
        return claim_and_run(
            gateway,
            args.token,
            owner=args.owner,
            mode=args.mode,
            audio=args.audio or None,
            backend=args.backend or None,
        )
    if not args.meeting:
        print("MEETING_ID / --meeting required (or pass --claim)", file=sys.stderr)
        return 2

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
