import type { JobStatus } from '../api/types';

/** The six lamps on the pipeline strip, in order. */
export const LAMPS = ['CONVERT', 'ANALYZE', 'SEPARATE', 'FINALIZE', 'PACKAGE', 'DONE'] as const;

export type LampState = 'off' | 'active' | 'done';

interface StageCopy {
  message: string;
  detail: string;
  /** Overall percentage at which this stage ends (design: stage copy table). */
  endsAt: number;
  lamp: number;
}

/** Stage copy from the upstream job model, kept verbatim. */
export const STAGE_COPY: Record<Exclude<JobStatus, 'failed' | 'cancelled'>, StageCopy> = {
  pending: { message: 'Waiting in the queue', detail: 'Processing starts as soon as a worker is free', endsAt: 0, lamp: -1 },
  converting: { message: 'Converting audio to WAV format...', detail: 'Converting to 24-bit/48kHz professional format', endsAt: 14, lamp: 0 },
  analyzing: { message: 'Analyzing tempo and detecting BPM...', detail: 'Using librosa to detect tempo and beats', endsAt: 28, lamp: 1 },
  separating: { message: 'Separating stems with AI...', detail: 'Using Demucs AI to separate vocals, drums, bass, and other instruments', endsAt: 76, lamp: 2 },
  finalizing: { message: 'Embedding metadata into stems...', detail: 'Adding tempo information to each stem file', endsAt: 89, lamp: 3 },
  packaging: { message: 'Creating download package...', detail: 'Bundling stems and creating DAWproject file', endsAt: 99, lamp: 4 },
  completed: { message: 'Processing complete', detail: 'Stems, DAWproject file and tempo map are ready', endsAt: 100, lamp: 5 },
};

const ORDER: JobStatus[] = ['pending', 'converting', 'analyzing', 'separating', 'finalizing', 'packaging', 'completed'];

export const ACTIVE_STATUSES: JobStatus[] = ['pending', 'converting', 'analyzing', 'separating', 'finalizing', 'packaging'];

export function isActive(status: JobStatus): boolean {
  return ACTIVE_STATUSES.includes(status);
}

export function isTerminal(status: JobStatus): boolean {
  return status === 'completed' || status === 'failed' || status === 'cancelled';
}

/** Cancel is offered until packaging starts. */
export function canCancel(status: JobStatus): boolean {
  return status === 'pending' || status === 'converting' || status === 'analyzing' || status === 'separating' || status === 'finalizing';
}

/** Index of the stage a status belongs to (pending = −1, completed = 6). */
export function stageIndex(status: JobStatus): number {
  const i = ORDER.indexOf(status);
  return i < 0 ? -1 : i - 1;
}

/**
 * Overall progress 0..100 from a status and the per-stage progress the
 * worker reports (0..100 within the stage).
 */
export function overallProgress(status: JobStatus, stageProgress: number): number {
  if (status === 'completed') return 100;
  if (status === 'pending') return 0;
  const copy = STAGE_COPY[status as keyof typeof STAGE_COPY];
  if (!copy) return Math.max(0, Math.min(100, stageProgress));
  const idx = ORDER.indexOf(status);
  const prev = idx > 1 ? STAGE_COPY[ORDER[idx - 1] as keyof typeof STAGE_COPY].endsAt : 0;
  const p = Math.max(0, Math.min(100, stageProgress)) / 100;
  return Math.round(prev + (copy.endsAt - prev) * p);
}

/**
 * Lamp states for a status. `frozenAt` is the last live stage for failed or
 * cancelled jobs (their own status carries no stage).
 */
export function lampStates(status: JobStatus, frozenAt: JobStatus | null = null): LampState[] {
  const s = status === 'failed' || status === 'cancelled' ? (frozenAt ?? 'separating') : status;
  const active = STAGE_COPY[s as keyof typeof STAGE_COPY]?.lamp ?? -1;
  return LAMPS.map((_, i) => {
    if (s === 'completed') return 'done';
    if (i < active) return 'done';
    if (i === active) return 'active';
    return 'off';
  });
}

/** Upper-case badge text: SEPARATING, COMPLETED, FAILED… */
export function statusLabel(status: JobStatus): string {
  return status.toUpperCase();
}

export type BadgeTone = 'running' | 'done' | 'failed' | 'outline';

export function badgeTone(status: JobStatus): BadgeTone {
  switch (status) {
    case 'completed':
      return 'done';
    case 'failed':
      return 'failed';
    case 'pending':
    case 'cancelled':
      return 'outline';
    default:
      return 'running';
  }
}

/** The row edge treatment on the job list. */
export function rowTone(status: JobStatus): 'running' | 'failed' | '' {
  if (status === 'failed') return 'failed';
  if (isActive(status)) return 'running';
  return '';
}

export function qualityLabel(q: string): string {
  switch (q) {
    case 'fast':
      return 'Fast';
    case 'high':
      return 'High quality';
    case 'high6':
      return 'High quality + guitar/piano';
    default:
      return q;
  }
}

/** Short badge form: FAST, HIGH QUALITY, HQ · 6 STEMS. */
export function qualityBadge(q: string): string {
  switch (q) {
    case 'fast':
      return 'FAST';
    case 'high':
      return 'HIGH QUALITY';
    case 'high6':
      return 'HQ · 6 STEMS';
    default:
      return q.toUpperCase();
  }
}

/** The mono caption under a strip name. */
export function stemModelCaption(quality: string, stem: string): string {
  if (stem === 'other') return 'GTR / KEYS';
  if (stem === 'guitar' || stem === 'piano') return 'DEMUCS 6S';
  return quality === 'fast' ? 'DEMUCS' : 'DEMUCS HT';
}

/** Which stage a failed or cancelled job stopped at, spelled for prose. */
export function stageNoun(status: JobStatus | null): string {
  switch (status) {
    case 'converting':
      return 'conversion';
    case 'analyzing':
      return 'tempo analysis';
    case 'separating':
      return 'stem separation';
    case 'finalizing':
      return 'finalising';
    case 'packaging':
      return 'packaging';
    case 'pending':
      return 'the queue';
    default:
      return 'stem separation';
  }
}
