//! 本机麦克风采集（cpal）。
//!
//! 架构 §5：录的是**员工自己的麦克风**，不是系统 loopback、不是整场混音。
//! 所以这里只取 `default_input_device()`，永远不去碰输出设备的回环。
//!
//! 采集线程把样本统一转成 **s16le PCM** 后通过 channel 送给写盘线程；
//! 音频回调里不做文件 IO，也不抢录音会话的锁（回调超时会爆音/丢帧）。
//!
//! 采样格式转换是纯函数，放在 feature 外面，没有麦克风也能单测。

use std::sync::atomic::AtomicBool;
use std::sync::mpsc::Sender;
use std::sync::Arc;
use std::thread::JoinHandle;

/// 采集到的 PCM 参数。我们固定落盘为 s16le raw PCM。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct AudioFormat {
    pub sample_rate: u32,
    pub channels: u16,
}

impl AudioFormat {
    /// 落盘固定 16-bit。
    pub const BYTES_PER_SAMPLE: usize = 2;

    pub fn bytes_per_second(&self) -> usize {
        self.sample_rate as usize * self.channels.max(1) as usize * Self::BYTES_PER_SAMPLE
    }
}

impl Default for AudioFormat {
    fn default() -> Self {
        // 缺省值只用于「没真开麦」的路径（降级构建 / 测试）。
        Self {
            sample_rate: 48_000,
            channels: 1,
        }
    }
}

/// f32 样本 → s16le 字节。超出 [-1, 1] 的样本先夹紧，避免回绕成刺啦声。
///
/// 降级构建里只有测试会调它（没有 cpal 就没有采集回调），所以那种情况下免掉 dead_code 警告。
#[cfg_attr(not(feature = "cpal"), allow(dead_code))]
pub fn f32_to_pcm16le(input: &[f32]) -> Vec<u8> {
    let mut out = Vec::with_capacity(input.len() * 2);
    for &s in input {
        let clamped = s.clamp(-1.0, 1.0);
        let v = (clamped * i16::MAX as f32) as i16;
        out.extend_from_slice(&v.to_le_bytes());
    }
    out
}

/// i16 样本 → s16le 字节（只是端序固定化）。
#[cfg_attr(not(feature = "cpal"), allow(dead_code))]
pub fn i16_to_pcm16le(input: &[i16]) -> Vec<u8> {
    let mut out = Vec::with_capacity(input.len() * 2);
    for &s in input {
        out.extend_from_slice(&s.to_le_bytes());
    }
    out
}

/// u16 无符号样本 → s16le：把 0..65535 平移到 -32768..32767。
#[cfg_attr(not(feature = "cpal"), allow(dead_code))]
pub fn u16_to_pcm16le(input: &[u16]) -> Vec<u8> {
    let mut out = Vec::with_capacity(input.len() * 2);
    for &s in input {
        let v = (s as i32 - 32_768) as i16;
        out.extend_from_slice(&v.to_le_bytes());
    }
    out
}

/// 采集句柄。Drop 即停流（RAII，避免忘了 stop 之后麦克风灯一直亮）。
///
/// 降级构建（`--no-default-features`）里没人会构造它，所以关掉 dead_code 警告：
/// 保留同一个类型是为了让上层代码不用写两套 `cfg`。
#[allow(dead_code)]
pub struct MicHandle {
    stop_tx: Option<Sender<()>>,
    join: Option<JoinHandle<()>>,
    format: AudioFormat,
}

impl MicHandle {
    pub fn format(&self) -> AudioFormat {
        self.format
    }

    /// 停止采集并等采集线程退出。可以重复调用。
    pub fn stop(&mut self) {
        // 发停止信号；接收端已经没了也无所谓
        if let Some(tx) = self.stop_tx.take() {
            let _ = tx.send(());
        }
        if let Some(join) = self.join.take() {
            let _ = join.join();
        }
    }
}

impl Drop for MicHandle {
    fn drop(&mut self) {
        self.stop();
    }
}

/// 打开默认输入设备开始采集。
///
/// - `paused`：为 true 时回调直接丢样本（会话仍存续，符合「静音不是暂停」的反向要求：
///   暂停期间不产生音频数据，并由上层写审计缺口）。
/// - `pcm_tx`：s16le PCM 字节流出口；接收端断开时采集线程自行退出。
#[cfg(feature = "cpal")]
pub fn spawn_default_mic(
    paused: Arc<AtomicBool>,
    pcm_tx: Sender<Vec<u8>>,
) -> Result<MicHandle, String> {
    use cpal::traits::{DeviceTrait, HostTrait, StreamTrait};
    use std::sync::mpsc;

    let (ready_tx, ready_rx) = mpsc::channel::<Result<AudioFormat, String>>();
    let (stop_tx, stop_rx) = mpsc::channel::<()>();

    // cpal::Stream 不是 Send，必须在自己的线程里建好并活到停止为止。
    let join = std::thread::Builder::new()
        .name("metuai-mic".into())
        .spawn(move || {
            let built = (|| -> Result<(cpal::Stream, AudioFormat), String> {
                let host = cpal::default_host();
                let device = host
                    .default_input_device()
                    .ok_or_else(|| "no default input device".to_string())?;
                let supported = device
                    .default_input_config()
                    .map_err(|e| format!("default_input_config: {e}"))?;
                let format = AudioFormat {
                    sample_rate: supported.sample_rate().0,
                    channels: supported.channels(),
                };
                let sample_format = supported.sample_format();
                let config: cpal::StreamConfig = supported.into();

                let err_fn = |e| {
                    // 不打印样本内容，只记设备错误（§5.3：录音不入日志）
                    eprintln!("[mic] stream error: {e}");
                };

                macro_rules! build {
                    ($ty:ty, $conv:path) => {{
                        let paused = Arc::clone(&paused);
                        let tx = pcm_tx.clone();
                        device
                            .build_input_stream(
                                &config,
                                move |data: &[$ty], _: &cpal::InputCallbackInfo| {
                                    if paused.load(std::sync::atomic::Ordering::Relaxed) {
                                        return;
                                    }
                                    // 回调里只做定长转换 + channel send，不做 IO / 不上锁
                                    let _ = tx.send($conv(data));
                                },
                                err_fn,
                                None,
                            )
                            .map_err(|e| format!("build_input_stream: {e}"))?
                    }};
                }

                let stream = match sample_format {
                    cpal::SampleFormat::F32 => build!(f32, f32_to_pcm16le),
                    cpal::SampleFormat::I16 => build!(i16, i16_to_pcm16le),
                    cpal::SampleFormat::U16 => build!(u16, u16_to_pcm16le),
                    other => return Err(format!("unsupported sample format: {other:?}")),
                };
                stream.play().map_err(|e| format!("stream.play: {e}"))?;
                Ok((stream, format))
            })();

            match built {
                Ok((stream, format)) => {
                    let _ = ready_tx.send(Ok(format));
                    // 阻塞等停止信号；发送端被丢弃也算停止
                    let _ = stop_rx.recv();
                    drop(stream);
                }
                Err(e) => {
                    let _ = ready_tx.send(Err(e));
                }
            }
        })
        .map_err(|e| format!("spawn mic thread: {e}"))?;

    match ready_rx.recv() {
        Ok(Ok(format)) => Ok(MicHandle {
            stop_tx: Some(stop_tx),
            join: Some(join),
            format,
        }),
        Ok(Err(e)) => {
            let _ = join.join();
            Err(e)
        }
        Err(_) => {
            let _ = join.join();
            Err("mic thread died before reporting readiness".into())
        }
    }
}

/// 降级构建（`--no-default-features`）：没有 cpal，直接告诉调用方开不了麦。
/// 状态机、分块、checksum、上传链路仍然可编译、可单测
/// （前端还能用 `append_local_pcm` 手动喂 PCM）。
#[cfg(not(feature = "cpal"))]
pub fn spawn_default_mic(
    _paused: Arc<AtomicBool>,
    _pcm_tx: Sender<Vec<u8>>,
) -> Result<MicHandle, String> {
    Err("built without the `cpal` feature: microphone capture unavailable".into())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn f32_conversion_is_little_endian_signed() {
        // 0.0 → 0；1.0 → i16::MAX；-1.0 → -32767
        let bytes = f32_to_pcm16le(&[0.0, 1.0, -1.0]);
        assert_eq!(bytes.len(), 6);
        assert_eq!(&bytes[0..2], &0i16.to_le_bytes());
        assert_eq!(&bytes[2..4], &i16::MAX.to_le_bytes());
        assert_eq!(&bytes[4..6], &(-i16::MAX).to_le_bytes());
    }

    #[test]
    fn f32_conversion_clamps_out_of_range() {
        // 超过 1.0 的样本如果不夹紧，转换后会回绕成反向大值（听起来是爆音）
        let bytes = f32_to_pcm16le(&[9.5, -9.5]);
        assert_eq!(&bytes[0..2], &i16::MAX.to_le_bytes());
        assert_eq!(&bytes[2..4], &(-i16::MAX).to_le_bytes());
    }

    #[test]
    fn i16_conversion_preserves_values() {
        let bytes = i16_to_pcm16le(&[0, 123, -456, i16::MIN]);
        assert_eq!(bytes.len(), 8);
        assert_eq!(&bytes[2..4], &123i16.to_le_bytes());
        assert_eq!(&bytes[4..6], &(-456i16).to_le_bytes());
    }

    #[test]
    fn u16_conversion_recenters_around_zero() {
        // 无符号中点 32768 就是静音
        let bytes = u16_to_pcm16le(&[32_768, 0, 65_535]);
        assert_eq!(&bytes[0..2], &0i16.to_le_bytes());
        assert_eq!(&bytes[2..4], &i16::MIN.to_le_bytes());
        assert_eq!(&bytes[4..6], &i16::MAX.to_le_bytes());
    }

    #[test]
    fn empty_input_yields_empty_output() {
        assert!(f32_to_pcm16le(&[]).is_empty());
        assert!(i16_to_pcm16le(&[]).is_empty());
        assert!(u16_to_pcm16le(&[]).is_empty());
    }

    #[test]
    fn bytes_per_second_matches_s16le_math() {
        let mono = AudioFormat {
            sample_rate: 48_000,
            channels: 1,
        };
        assert_eq!(mono.bytes_per_second(), 96_000);
        let stereo = AudioFormat {
            sample_rate: 44_100,
            channels: 2,
        };
        assert_eq!(stereo.bytes_per_second(), 176_400);
    }
}
