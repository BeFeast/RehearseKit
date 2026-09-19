import type { JobStatus } from '../api/types';
import { badgeTone, qualityBadge, statusLabel, type BadgeTone } from '../lib/stages';

export function Badge({ tone = 'outline', children, title }: { tone?: BadgeTone; children: React.ReactNode; title?: string }) {
  return (
    <span className={`rk-badge rk-badge--${tone}`} title={title}>
      {children}
    </span>
  );
}

export function StatusBadge({ status }: { status: JobStatus }) {
  return <Badge tone={badgeTone(status)}>{statusLabel(status)}</Badge>;
}

export function QualityBadge({ quality }: { quality: string }) {
  return <Badge tone="outline">{qualityBadge(quality)}</Badge>;
}

export function Progress({ value, indeterminate = false, label }: { value?: number; indeterminate?: boolean; label?: string }) {
  if (indeterminate) {
    return (
      <div className="rk-progress rk-progress--indeterminate" role="progressbar" aria-label={label ?? 'Queued'}>
        <span />
      </div>
    );
  }
  const v = Math.max(0, Math.min(100, Math.round(value ?? 0)));
  return (
    <div className="rk-progress" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={v} aria-label={label}>
      <span style={{ width: `${v}%` }} />
    </div>
  );
}

export function Skeleton({ height, width, style }: { height: number; width?: string | number; style?: React.CSSProperties }) {
  return <div className="rk-skel" style={{ height, width, ...style }} aria-hidden="true" />;
}
