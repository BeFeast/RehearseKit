/** "0:58", "12:03", "1:02:15" — timecode from seconds. */
export function formatTimecode(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) seconds = 0;
  const total = Math.floor(seconds);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const ss = String(s).padStart(2, '0');
  if (h > 0) return `${h}:${String(m).padStart(2, '0')}:${ss}`;
  return `${m}:${ss}`;
}

/** 1-based bar number at time t for a 4/4 grid at bpm. */
export function barOf(t: number, bpm: number, beatsPerBar = 4): number {
  if (!bpm || bpm <= 0 || !Number.isFinite(t)) return 1;
  return Math.floor((t * bpm) / 60 / beatsPerBar) + 1;
}

/** "3.2" — bar.beat position, 1-based. */
export function formatBarsBeats(t: number, bpm: number, beatsPerBar = 4): string {
  if (!bpm || bpm <= 0) return formatTimecode(t);
  const beats = Math.floor((Math.max(0, t) * bpm) / 60);
  const bar = Math.floor(beats / beatsPerBar) + 1;
  const beat = (beats % beatsPerBar) + 1;
  return `${bar}.${beat}`;
}

/** Time of the beat grid line nearest to t (for loop-handle snapping). */
export function snapToBeat(t: number, bpm: number | null | undefined): number {
  if (!bpm || bpm <= 0) return t;
  const beat = 60 / bpm;
  return Math.round(t / beat) * beat;
}

/** "BARS 24–44" when bpm is known, otherwise "LOOP 0:46 → 1:24". */
export function formatLoopLabel(a: number, b: number, bpm: number | null | undefined): string {
  if (bpm && bpm > 0) return `BARS ${barOf(a, bpm)}–${barOf(b, bpm)}`;
  return `LOOP ${formatTimecode(a)} → ${formatTimecode(b)}`;
}

/** "LOOP 0:46 → 1:24 · 0:38" — the eyebrow beside the waveform title. */
export function formatLoopSummary(a: number, b: number): string {
  return `LOOP ${formatTimecode(a)} → ${formatTimecode(b)} · ${formatTimecode(b - a)}`;
}

export function formatBpm(bpm: number | null | undefined): string {
  if (!bpm || bpm <= 0) return '—';
  return bpm.toFixed(1);
}

const KB = 1024;
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '0 B';
  if (bytes < KB) return `${bytes} B`;
  if (bytes < KB * KB) return `${(bytes / KB).toFixed(0)} KB`;
  if (bytes < KB * KB * KB) return `${(bytes / KB / KB).toFixed(bytes < 10 * KB * KB ? 1 : 0)} MB`;
  return `${(bytes / KB / KB / KB).toFixed(2)} GB`;
}

/** "about 40s left", "about 2 min left". */
export function formatEta(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '';
  if (seconds < 60) return `about ${Math.max(1, Math.round(seconds))}s left`;
  const m = Math.round(seconds / 60);
  return `about ${m} min left`;
}

/** "12 min ago", "2 hours ago", "yesterday", "3 days ago", "14 Mar 2026". */
export function formatRelative(iso: string, now: Date = new Date()): string {
  const then = new Date(iso);
  const diff = Math.max(0, now.getTime() - then.getTime()) / 1000;
  if (diff < 45) return 'just now';
  if (diff < 3600) return `${Math.round(diff / 60)} min ago`;
  if (diff < 86400) {
    const h = Math.round(diff / 3600);
    return `${h} hour${h === 1 ? '' : 's'} ago`;
  }
  const days = Math.round(diff / 86400);
  if (days === 1) return 'yesterday';
  if (days < 7) return `${days} days ago`;
  return formatDate(iso);
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const MONTHS_LONG = [
  'January', 'February', 'March', 'April', 'May', 'June',
  'July', 'August', 'September', 'October', 'November', 'December',
];

/** "14 Mar 2026" */
export function formatDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return `${d.getDate()} ${MONTHS[d.getMonth()]} ${d.getFullYear()}`;
}

/** "14 March 2026" */
export function formatDateLong(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return `${d.getDate()} ${MONTHS_LONG[d.getMonth()]} ${d.getFullYear()}`;
}

/** "Today at 09:12", otherwise "14 Mar 2026 09:12". */
export function formatDateTime(iso: string | null | undefined, now: Date = new Date()): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  const hm = `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
  const sameDay = d.toDateString() === now.toDateString();
  return sameDay ? `Today at ${hm}` : `${formatDate(iso)} ${hm}`;
}

/** Hours until an ISO timestamp, floored at 0. */
export function hoursUntil(iso: string, now: Date = new Date()): number {
  const ms = new Date(iso).getTime() - now.getTime();
  return Math.max(0, Math.round(ms / 3600000));
}

/** "DW" from "Dana Whitfield"; falls back to the email's first letters. */
export function initials(name: string, email = ''): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length >= 2) return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
  if (parts.length === 1 && parts[0].length >= 2) return parts[0].slice(0, 2).toUpperCase();
  if (parts.length === 1) return parts[0].toUpperCase();
  return email.slice(0, 2).toUpperCase() || '?';
}

/** "youtube.com/watch?v=dQw4w9WgXcQ" — a URL shortened for a source line. */
export function shortUrl(url: string): string {
  try {
    const u = new URL(url);
    return `${u.hostname.replace(/^www\./, '')}${u.pathname}${u.search}`;
  } catch {
    return url;
  }
}
