import { useEffect, useRef, useState } from 'react';
import { formatBytes, formatTimecode } from '../../lib/format';

export interface FileInfo {
  ext: string;
  /** "WAV · 612 MB" plus bit depth / rate / duration once decoded. */
  line: string;
  duration: number | null;
}

const AUDIO_EXT = new Set(['wav', 'mp3', 'flac', 'm4a', 'aac', 'ogg', 'opus', 'aiff', 'aif', 'wma']);
const VIDEO_EXT = new Set(['mp4', 'mov', 'mkv', 'webm', 'avi', 'm4v']);

/** Specific reasons, never "invalid file" (file-dropzone spec). */
export function validateFile(file: File, maxBytes: number): string | null {
  const ext = file.name.split('.').pop()?.toLowerCase() ?? '';
  const limit = formatBytes(maxBytes);
  if (VIDEO_EXT.has(ext)) {
    return `${file.name} is a video file. RehearseKit accepts WAV, MP3, FLAC and AIFF up to ${limit} — extract the audio and try again.`;
  }
  if (!AUDIO_EXT.has(ext)) {
    return `${file.name} is not an audio file RehearseKit can read. It accepts WAV, MP3, FLAC and AIFF up to ${limit}.`;
  }
  if (file.size === 0) return `${file.name} is empty.`;
  if (file.size > maxBytes) {
    return `${file.name} is ${formatBytes(file.size)}. RehearseKit accepts files up to ${limit} — export a shorter section or a compressed version.`;
  }
  return null;
}

export function describeFile(file: File): FileInfo {
  const ext = (file.name.split('.').pop() ?? '').toUpperCase();
  return { ext, line: `${ext} · ${formatBytes(file.size)}`, duration: null };
}

/** Decode the first N seconds fully for small files, a prefix for big ones. */
const FULL_DECODE_BYTES = 64 * 1024 * 1024;
const PREVIEW_SECONDS = 60;
const COLUMNS = 200;

interface Decoded {
  columns: number[];
  duration: number;
  sampleRate: number;
  bits: number | null;
  partial: boolean;
  buffer: AudioBuffer;
}

/**
 * components/… "Audio Preview": decoded locally via decodeAudioData, drawn
 * as a mini waveform; plays in place. Nothing is uploaded to preview.
 */
export function AudioPreview({ file, info }: { file: File; info: FileInfo | null }) {
  const [decoded, setDecoded] = useState<Decoded | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [playing, setPlaying] = useState(false);
  const [pos, setPos] = useState(0);
  const ctxRef = useRef<AudioContext | null>(null);
  const srcRef = useRef<AudioBufferSourceNode | null>(null);
  const startedAt = useRef(0);
  const raf = useRef(0);

  useEffect(() => {
    let cancelled = false;
    setDecoded(null);
    setError(null);
    setPlaying(false);
    setPos(0);
    (async () => {
      try {
        const partial = file.size > FULL_DECODE_BYTES;
        const blob = partial ? file.slice(0, FULL_DECODE_BYTES) : file;
        const bytes = await blob.arrayBuffer();
        const ctx = ctxRef.current ?? new AudioContext();
        ctxRef.current = ctx;
        const buf = await ctx.decodeAudioData(bytes.slice(0));
        if (cancelled) return;
        const seconds = partial ? Math.min(buf.duration, PREVIEW_SECONDS) : buf.duration;
        const frames = Math.floor(seconds * buf.sampleRate);
        const cols = new Array<number>(COLUMNS).fill(0);
        const per = Math.max(1, Math.floor(frames / COLUMNS));
        for (let c = 0; c < buf.numberOfChannels; c++) {
          const d = buf.getChannelData(c);
          for (let x = 0; x < COLUMNS; x++) {
            let peak = 0;
            const s = x * per;
            const e = Math.min(frames, s + per);
            for (let i = s; i < e; i += 4) {
              const v = Math.abs(d[i]);
              if (v > peak) peak = v;
            }
            if (peak > cols[x]) cols[x] = peak;
          }
        }
        const bits = wavBits(bytes);
        setDecoded({ columns: cols, duration: buf.duration, sampleRate: buf.sampleRate, bits, partial, buffer: buf });
      } catch {
        if (!cancelled) setError('Could not decode this file for a preview — the server will still try to process it.');
      }
    })();
    return () => {
      cancelled = true;
      stop();
    };
  }, [file]);

  useEffect(
    () => () => {
      void ctxRef.current?.close();
      ctxRef.current = null;
    },
    [],
  );

  function stop() {
    cancelAnimationFrame(raf.current);
    try {
      srcRef.current?.stop();
    } catch {
      // already stopped
    }
    srcRef.current = null;
    setPlaying(false);
  }

  function play() {
    const ctx = ctxRef.current;
    if (!ctx || !decoded) return;
    if (playing) {
      stop();
      return;
    }
    void ctx.resume();
    const src = ctx.createBufferSource();
    src.buffer = decoded.buffer;
    src.connect(ctx.destination);
    const from = pos >= decoded.buffer.duration - 0.05 ? 0 : pos;
    src.start(0, from);
    startedAt.current = ctx.currentTime - from;
    srcRef.current = src;
    setPlaying(true);
    src.onended = () => {
      if (srcRef.current === src) {
        setPlaying(false);
        srcRef.current = null;
        setPos(0);
      }
    };
    const tick = () => {
      if (srcRef.current !== src) return;
      setPos(ctx.currentTime - startedAt.current);
      raf.current = requestAnimationFrame(tick);
    };
    raf.current = requestAnimationFrame(tick);
  }

  const duration = decoded?.duration ?? info?.duration ?? null;
  const shownDuration = decoded ? (decoded.partial ? decoded.buffer.duration : decoded.duration) : null;
  const techLine = decoded
    ? `${info?.ext ?? ''} · ${decoded.bits ? `${decoded.bits} bit / ` : ''}${(decoded.sampleRate / 1000).toFixed(decoded.sampleRate % 1000 ? 1 : 0)} kHz · ${formatBytes(file.size)}${duration ? ` · ${formatTimecode(duration)}${decoded.partial ? '+' : ''}` : ''}`
    : null;

  return (
    <div className="rk-flat" style={{ padding: 'var(--rk-space-7)', display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-5)' }} data-testid="audio-preview">
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 'var(--rk-space-6)' }}>
        <span className="rk-eyebrow">AUDIO PREVIEW</span>
        <span className="rk-mono rk-muted" style={{ fontSize: 'var(--rk-font-size-md)' }}>
          {shownDuration != null ? `${formatTimecode(pos)} / ${formatTimecode(shownDuration)}${decoded?.partial ? ' (first minute)' : ''}` : decoded === null && !error ? 'decoding…' : ''}
        </span>
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--rk-space-6)' }}>
        <button className="rk-tbtn rk-tbtn--play" type="button" aria-label={playing ? 'Pause preview' : 'Play preview'} aria-pressed={playing} style={{ flex: 'none' }} onClick={play} disabled={!decoded}>
          {playing ? (
            <span style={{ display: 'flex', gap: 4 }}>
              <i style={{ display: 'block', width: 4, height: 15, background: 'currentColor' }} />
              <i style={{ display: 'block', width: 4, height: 15, background: 'currentColor' }} />
            </span>
          ) : (
            <span style={{ display: 'block', width: 0, height: 0, borderLeft: '12px solid currentColor', borderTop: '8px solid transparent', borderBottom: '8px solid transparent' }} />
          )}
        </button>
        <div className="rk-miniwave" style={{ flex: 1 }}>
          {decoded ? (
            <svg viewBox={`0 0 ${COLUMNS * 4} 56`} preserveAspectRatio="none" aria-hidden="true" fill="var(--rk-color-wave-main)">
              {decoded.columns.map((v, i) => {
                const h = Math.max(1.5, v * 52);
                return <rect key={i} x={i * 4} y={28 - h / 2} width={3} height={h} />;
              })}
              <rect x={0} y={27.5} width={COLUMNS * 4} height={1} opacity={0.35} />
            </svg>
          ) : (
            <div className="rk-skel" style={{ position: 'absolute', inset: 0, borderRadius: 0 }} />
          )}
          {decoded && shownDuration ? <div className="rk-playhead" style={{ left: `${Math.min(100, (pos / shownDuration) * 100)}%` }} /> : null}
        </div>
      </div>
      <span className="rk-help">
        {error ?? (techLine ? `${file.name} · ${techLine} · preview plays locally, nothing is uploaded until you submit` : `${file.name} · preview plays locally, nothing is uploaded until you submit`)}
      </span>
    </div>
  );
}

/** Bits per sample from a RIFF/WAVE fmt chunk, if the bytes are a WAV. */
function wavBits(buf: ArrayBuffer): number | null {
  if (buf.byteLength < 36) return null;
  const v = new DataView(buf);
  const tag = (o: number) => String.fromCharCode(v.getUint8(o), v.getUint8(o + 1), v.getUint8(o + 2), v.getUint8(o + 3));
  if (tag(0) !== 'RIFF' || tag(8) !== 'WAVE') return null;
  let off = 12;
  while (off + 8 <= buf.byteLength) {
    const id = tag(off);
    const size = v.getUint32(off + 4, true);
    if (id === 'fmt ' && off + 24 <= buf.byteLength) return v.getUint16(off + 22, true);
    off += 8 + size + (size & 1);
  }
  return null;
}
