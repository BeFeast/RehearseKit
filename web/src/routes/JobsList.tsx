import { useEffect, useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import * as api from '../api';
import { errorMessage } from '../api/client';
import type { Job, JobListStatus } from '../api/types';
import { EmptyState } from '../components/EmptyState';
import { ConfirmDialog } from '../components/Dialog';
import { Icon } from '../components/Icon';
import { JobRow, JobRowSkeleton } from '../components/JobRow';
import { useToast } from '../components/Toast';
import { isActive, overallProgress } from '../lib/stages';
import { useDebounced } from '../lib/use-debounced';
import { jobsRoute } from '../router';
import { RequireUser } from './guards';

export interface JobsSearch {
  status?: JobListStatus;
  q?: string;
  page?: number;
}

export function jobsSearchSchema(raw: Record<string, unknown>): JobsSearch {
  const out: JobsSearch = {};
  if (raw.status === 'active' || raw.status === 'completed' || raw.status === 'failed') out.status = raw.status;
  if (typeof raw.q === 'string' && raw.q) out.q = raw.q;
  const p = Number(raw.page);
  if (Number.isInteger(p) && p > 1) out.page = p;
  return out;
}

/** 6 per page, newest first (screens/04-jobs-list/notes.md). */
export const PAGE_SIZE = 6;

const FILTERS: { id: JobListStatus; label: string }[] = [
  { id: 'all', label: 'ALL' },
  { id: 'active', label: 'ACTIVE' },
  { id: 'completed', label: 'COMPLETED' },
  { id: 'failed', label: 'FAILED' },
];

export function JobsListRoute() {
  return (
    <RequireUser title="Job History">
      <JobsListPage />
    </RequireUser>
  );
}

function JobsListPage() {
  const navigate = useNavigate();
  const search = jobsRoute.useSearch();
  const qc = useQueryClient();
  const { toast } = useToast();
  const status: JobListStatus = search.status ?? 'all';
  const page = search.page ?? 1;
  const [q, setQ] = useState(search.q ?? '');
  const debouncedQ = useDebounced(q, 250);
  const [confirm, setConfirm] = useState<{ kind: 'cancel' | 'delete'; job: Job } | null>(null);

  useEffect(() => {
    document.title = 'Job History — RehearseKit';
  }, []);

  // Reflect the debounced search in the URL so a filtered list can be linked.
  useEffect(() => {
    if ((search.q ?? '') !== debouncedQ) {
      void navigate({ to: '/jobs', search: { ...search, q: debouncedQ || undefined, page: undefined }, replace: true });
    }
  }, [debouncedQ, navigate, search]);

  const setSearch = (patch: Partial<JobsSearch>) => void navigate({ to: '/jobs', search: { ...search, ...patch } });

  const jobs = useQuery({
    queryKey: ['jobs', 'list', status, debouncedQ, page],
    queryFn: () => api.listJobs({ status, q: debouncedQ || undefined, page, page_size: PAGE_SIZE }),
    placeholderData: keepPreviousData,
    // Running jobs poll while the page is visible; polling pauses when hidden.
    refetchInterval: (query) => (query.state.data?.items.some((j) => isActive(j.status)) ? 3000 : false),
    refetchIntervalInBackground: false,
  });
  const running = useQuery({
    queryKey: ['jobs', 'list', 'active-count'],
    queryFn: () => api.listJobs({ status: 'active', page: 1, page_size: 1 }),
    refetchInterval: 10_000,
  });
  const all = useQuery({
    queryKey: ['jobs', 'list', 'all-count'],
    queryFn: () => api.listJobs({ status: 'all', page: 1, page_size: 1 }),
  });

  const invalidate = () => qc.invalidateQueries({ queryKey: ['jobs'] });

  const cancel = useMutation({
    mutationFn: (job: Job) => api.cancelJob(job.id),
    onSuccess: () => void invalidate(),
    onError: (err) => toast({ kind: 'error', title: 'Could not cancel the job', detail: errorMessage(err) }),
  });
  const del = useMutation({
    mutationFn: (job: Job) => api.deleteJob(job.id),
    onSuccess: () => {
      void invalidate();
      toast({ kind: 'success', title: 'Job deleted' });
    },
    onError: (err) => toast({ kind: 'error', title: 'Could not delete the job', detail: errorMessage(err) }),
  });

  async function download(job: Job) {
    if (await api.downloadAvailable(job.id)) {
      window.location.assign(api.downloadUrl(job.id));
      toast({ kind: 'info', title: 'Download started', detail: `${job.project_name} — stems, DAWproject file and tempo map.` });
    } else {
      toast({ kind: 'error', title: 'Package download is not available yet', detail: 'This build serves stems for playback only; the zip package lands with the worker.' });
    }
  }
  async function copyLink(job: Job) {
    const url = `${window.location.origin}/jobs/${job.id}`;
    try {
      await navigator.clipboard.writeText(url);
      toast({ kind: 'success', title: 'Link copied', detail: url });
    } catch {
      toast({ kind: 'info', title: 'Job link', detail: url });
    }
  }

  const total = jobs.data?.total ?? 0;
  const items = jobs.data?.items ?? [];
  const totalAll = all.data?.total ?? total;
  const runningCount = running.data?.total ?? 0;
  const filtered = status !== 'all' || Boolean(debouncedQ);
  const from = total === 0 ? 0 : (page - 1) * PAGE_SIZE + 1;
  const to = Math.min(total, page * PAGE_SIZE);
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  const subtitle = jobs.isPending
    ? 'Loading jobs…'
    : totalAll === 0
      ? 'No jobs yet'
      : `${totalAll} job${totalAll === 1 ? '' : 's'}${runningCount ? ` · ${runningCount} running` : ''}`;

  return (
    <main className="rk-shell" style={{ paddingTop: 'var(--rk-space-11)', position: 'relative' }}>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', gap: 'var(--rk-space-8)', marginBottom: 'var(--rk-space-9)', flexWrap: 'wrap' }}>
        <div>
          <h1 className="rk-title">Job History</h1>
          <p className="rk-help" style={{ margin: 'var(--rk-space-3) 0 0' }}>
            {subtitle}
          </p>
        </div>
        <button className="rk-btn rk-btn--primary" type="button" onClick={() => void navigate({ to: '/' })}>
          <Icon name="plus" /> New job
        </button>
      </div>

      {!jobs.isPending && totalAll === 0 && !filtered ? (
        <EmptyState
          title="No jobs yet"
          action={
            <button className="rk-btn rk-btn--primary" type="button" onClick={() => void navigate({ to: '/' })}>
              Upload a track
            </button>
          }
        >
          Upload a recording or paste a YouTube link, and the stems will show up here. Jobs stay in your history until you delete them.
        </EmptyState>
      ) : (
        <>
          <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--rk-space-6)', marginBottom: 'var(--rk-space-8)', flexWrap: 'wrap' }}>
            <div className="rk-field" style={{ flex: 1, minWidth: 220, maxWidth: 320 }}>
              <label htmlFor="jobs-q" className="visually-hidden">
                Search jobs
              </label>
              <input className="rk-input" id="jobs-q" type="search" placeholder="Search by name or source" value={q} onChange={(e) => setQ(e.target.value)} />
            </div>
            <div className="rk-seg" role="group" aria-label="Filter by status">
              {FILTERS.map((f) => (
                <button key={f.id} type="button" aria-pressed={status === f.id} onClick={() => setSearch({ status: f.id === 'all' ? undefined : f.id, page: undefined })}>
                  {f.label}
                </button>
              ))}
            </div>
          </div>

          {jobs.isPending ? (
            <div className="rk-joblist" aria-busy="true">
              {[0, 1, 2, 3, 4].map((i) => (
                <JobRowSkeleton key={i} />
              ))}
            </div>
          ) : jobs.isError ? (
            <EmptyState art="error" title="Could not load your jobs" action={<button className="rk-btn" type="button" onClick={() => void jobs.refetch()}>Try again</button>}>
              {errorMessage(jobs.error)}
            </EmptyState>
          ) : items.length === 0 ? (
            <EmptyState
              title="No jobs match this filter"
              action={
                <button className="rk-btn" type="button" onClick={() => { setQ(''); setSearch({ status: undefined, q: undefined, page: undefined }); }}>
                  Clear filters
                </button>
              }
            >
              {debouncedQ
                ? `Nothing ${status === 'all' ? '' : status + ' '}matches “${debouncedQ}”. Try a different status, or clear the search to see all ${totalAll} jobs.`
                : `Nothing is ${status} right now. Try a different status to see all ${totalAll} jobs.`}
            </EmptyState>
          ) : (
            <>
              <div className="rk-joblist">
                {items.map((job) => (
                  <JobRow
                    key={job.id}
                    job={job}
                    busy={(cancel.isPending && cancel.variables?.id === job.id) || (del.isPending && del.variables?.id === job.id)}
                    onCancel={(j) => setConfirm({ kind: 'cancel', job: j })}
                    onDelete={(j) => setConfirm({ kind: 'delete', job: j })}
                    onDownload={(j) => void download(j)}
                    onCopyLink={(j) => void copyLink(j)}
                  />
                ))}
              </div>
              <div className="rk-pager">
                <span className="rk-help">
                  Showing {from}–{to} of {total} job{total === 1 ? '' : 's'}
                </span>
                <div className="rk-pagerbtns">
                  <button className="rk-btn rk-btn--sm" type="button" disabled={page <= 1} onClick={() => setSearch({ page: page - 1 > 1 ? page - 1 : undefined })}>
                    <Icon name="chevron-left" /> Previous
                  </button>
                  <button className="rk-btn rk-btn--sm" type="button" disabled={page >= pages} onClick={() => setSearch({ page: page + 1 })}>
                    Next <Icon name="chevron-right" />
                  </button>
                </div>
              </div>
            </>
          )}
        </>
      )}

      <ConfirmDialog
        open={confirm?.kind === 'cancel'}
        title="Cancel this job?"
        body={
          confirm
            ? `${confirm.job.project_name} is ${overallProgress(confirm.job.status, confirm.job.stage_progress)}% through processing. Cancelling discards the work done so far; the source file is kept so you can start it again.`
            : ''
        }
        cancelLabel="Keep Processing"
        confirmLabel="Cancel Job"
        danger
        pending={cancel.isPending}
        onCancel={() => setConfirm(null)}
        onConfirm={() => {
          if (confirm) cancel.mutate(confirm.job);
          setConfirm(null);
        }}
      />
      <ConfirmDialog
        open={confirm?.kind === 'delete'}
        title="Delete this job?"
        body={
          confirm
            ? `${confirm.job.project_name}${confirm.job.stems.length ? ` and its ${numberWord(confirm.job.stems.length)} stems` : ''} will be removed from storage. Anything you have already downloaded is unaffected. This cannot be undone.`
            : ''
        }
        cancelLabel="Keep Job"
        confirmLabel="Delete Job"
        danger
        pending={del.isPending}
        onCancel={() => setConfirm(null)}
        onConfirm={() => {
          if (confirm) del.mutate(confirm.job);
          setConfirm(null);
        }}
      />
    </main>
  );
}

function numberWord(n: number): string {
  const words = ['zero', 'one', 'two', 'three', 'four', 'five', 'six'];
  return words[n] ?? String(n);
}
