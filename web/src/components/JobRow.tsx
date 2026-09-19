import { useEffect, useState } from 'react';
import { Link } from '@tanstack/react-router';
import type { Job } from '../api/types';
import { formatRelative, formatTimecode, shortUrl } from '../lib/format';
import { canCancel, isActive, overallProgress, qualityLabel, rowTone, STAGE_COPY } from '../lib/stages';
import { Progress, StatusBadge } from './Badge';
import { Icon } from './Icon';

export interface JobRowProps {
  job: Job;
  /** Last live stage for failed/cancelled rows (from the events replay), if known. */
  onCancel(job: Job): void;
  onDelete(job: Job): void;
  onDownload(job: Job): void;
  onCopyLink(job: Job): void;
  busy?: boolean;
}

/** components/job-row: card with progress bar and stage sentence. */
export function JobRow({ job, onCancel, onDelete, onDownload, onCopyLink, busy = false }: JobRowProps) {
  const pct = overallProgress(job.status, job.stage_progress);
  const active = isActive(job.status);
  const stage = STAGE_COPY[job.status as keyof typeof STAGE_COPY];
  const source = job.input_type === 'youtube' ? shortUrl(job.input_url ?? '') : job.source_filename ?? '—';
  return (
    <article className="rk-jobrow" data-status={rowTone(job.status)} data-testid="job-row">
      <div style={{ minWidth: 0 }}>
        <h3 className="rk-jobrow-title">
          <Link to="/jobs/$id" params={{ id: job.id }} style={{ color: 'inherit' }}>
            {job.project_name}
          </Link>
        </h3>
        <div className="rk-jobrow-src">
          <Icon name={job.input_type === 'youtube' ? 'youtube' : 'file-audio'} size={14} />
          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{source}</span>
        </div>
      </div>
      <div className="rk-jobrow-meta">
        <span title={new Date(job.created_at).toLocaleString()}>{formatRelative(job.created_at)}</span>
        <span>{job.duration_seconds != null ? formatTimecode(job.duration_seconds) : '—'}</span>
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-3)' }}>
        <StatusBadge status={job.status} />
        {job.status === 'pending' ? (
          <>
            <Progress indeterminate label="Queued" />
            <span className="rk-help">Waiting for a worker</span>
          </>
        ) : active ? (
          <>
            <Progress value={pct} />
            <span className="rk-help">
              {pct}% · {stage?.message ?? ''}
            </span>
          </>
        ) : job.status === 'completed' ? (
          <span className="rk-mono rk-muted" style={{ fontSize: 'var(--rk-font-size-md)' }}>
            {job.detected_bpm != null ? `${job.detected_bpm.toFixed(1)} BPM` : 'BPM not detected'} · {qualityLabel(job.quality)}
          </span>
        ) : job.status === 'failed' ? (
          <span className="rk-help">{job.error || 'Processing failed'}</span>
        ) : (
          <span className="rk-help">Stopped at {pct}%</span>
        )}
      </div>
      <div className="rk-jobrow-actions" style={{ display: 'flex', gap: 'var(--rk-space-4)', justifyContent: 'flex-end' }}>
        {canCancel(job.status) ? (
          <button className="rk-btn rk-btn--sm" type="button" onClick={() => onCancel(job)} disabled={busy} aria-busy={busy || undefined}>
            Cancel Job
          </button>
        ) : job.status === 'completed' ? (
          <button className="rk-btn rk-btn--sm" type="button" onClick={() => onDownload(job)} disabled={busy}>
            <Icon name="download" /> Download
          </button>
        ) : job.status === 'packaging' ? (
          <button className="rk-btn rk-btn--sm" type="button" disabled>
            Packaging…
          </button>
        ) : (
          <Link className="rk-btn rk-btn--sm" to="/jobs/$id" params={{ id: job.id }} style={{ color: 'inherit' }}>
            <Icon name="refresh" /> Retry
          </Link>
        )}
        <KebabMenu job={job} onDelete={onDelete} onDownload={onDownload} onCopyLink={onCopyLink} />
      </div>
    </article>
  );
}

function KebabMenu({ job, onDelete, onDownload, onCopyLink }: Pick<JobRowProps, 'job' | 'onDelete' | 'onDownload' | 'onCopyLink'>) {
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (!open) return;
    const close = () => setOpen(false);
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', close);
    document.addEventListener('keydown', key);
    return () => {
      document.removeEventListener('mousedown', close);
      document.removeEventListener('keydown', key);
    };
  }, [open]);
  return (
    <div style={{ position: 'relative' }} onMouseDown={(e) => e.stopPropagation()}>
      <button className="rk-iconbtn" type="button" aria-label={`More actions for ${job.project_name}`} aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <Icon name="more" />
      </button>
      {open && (
        <div className="rk-menu rk-menu--anchored" role="menu">
          <button className="rk-menu-item" role="menuitem" type="button" disabled={job.status !== 'completed'} onClick={() => { setOpen(false); onDownload(job); }}>
            <Icon name="download" /> Download package
          </button>
          <button className="rk-menu-item" role="menuitem" type="button" onClick={() => { setOpen(false); onCopyLink(job); }}>
            <Icon name="link" /> Copy job link
          </button>
          <hr />
          <button className="rk-menu-item" role="menuitem" type="button" data-danger="true" onClick={() => { setOpen(false); onDelete(job); }}>
            <Icon name="trash" /> Delete job
          </button>
        </div>
      )}
    </div>
  );
}

export function JobRowSkeleton() {
  return (
    <article className="rk-jobrow" aria-hidden="true">
      <div>
        <div className="rk-skel" style={{ height: 15, width: '62%' }} />
        <div className="rk-skel" style={{ height: 11, width: '40%', marginTop: 'var(--rk-space-4)' }} />
      </div>
      <div className="rk-skel" style={{ height: 11, width: 100 }} />
      <div className="rk-skel" style={{ height: 34 }} />
      <div className="rk-skel" style={{ height: 26, width: 120, justifySelf: 'end' }} />
    </article>
  );
}
