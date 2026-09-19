import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { keepPreviousData, useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query';
import * as api from '../api';
import { ApiError, errorMessage } from '../api/client';
import type { Job, JobListStatus } from '../api/types';
import { useAuth } from '../auth/AuthProvider';
import { Skeleton } from '../components/Badge';
import { EmptyState } from '../components/EmptyState';
import { ConfirmDialog } from '../components/Dialog';
import { Icon } from '../components/Icon';
import { JobRow, JobRowSkeleton } from '../components/JobRow';
import { useToast } from '../components/Toast';
import { allClaims, forgetClaim } from '../lib/claim-tokens';
import { safeNext } from '../lib/isolation';
import { isActive, overallProgress } from '../lib/stages';
import { useAppNavigate } from '../lib/use-app-navigate';
import { useDebounced } from '../lib/use-debounced';
import { jobsRoute } from '../router';

export interface JobsSearch {
  status?: JobListStatus;
  q?: string;
  page?: number;
  /** Set by the job page (cross-origin isolated, no Google popup): open the dialog here. */
  signin?: 1;
  /** Where to return after the sign-in the job page asked for. */
  next?: string;
}

export function jobsSearchSchema(raw: Record<string, unknown>): JobsSearch {
  const out: JobsSearch = {};
  if (raw.status === 'active' || raw.status === 'completed' || raw.status === 'failed') out.status = raw.status;
  if (typeof raw.q === 'string' && raw.q) out.q = raw.q;
  const p = Number(raw.page);
  if (Number.isInteger(p) && p > 1) out.page = p;
  if (raw.signin === 1 || raw.signin === '1' || raw.signin === true) out.signin = 1;
  const next = safeNext(typeof raw.next === 'string' ? raw.next : null);
  if (next) out.next = next;
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

/**
 * /jobs. Signed in: the account's job history. Signed out: the anonymous
 * jobs this browser holds claim tokens for — no sign-in wall, the dialog is
 * an offer. `?signin=1&next=…` is the hand-off from the job page, whose
 * COOP headers block the Google popup: open the dialog here and return.
 */
export function JobsListRoute() {
  const { user, loading, openSignIn } = useAuth();
  const search = jobsRoute.useSearch();
  const navigate = useNavigate();
  const handled = useRef(false);

  useEffect(() => {
    document.title = 'Job History — RehearseKit';
  }, []);

  useEffect(() => {
    if (loading || !search.signin || handled.current) return;
    handled.current = true;
    const next = search.next ? safeNext(search.next) : null;
    const back = next ? () => window.location.assign(next) : undefined;
    void navigate({ to: '/jobs', search: { ...search, signin: undefined, next: undefined }, replace: true });
    if (user) back?.();
    else openSignIn(back);
  }, [loading, search, user, navigate, openSignIn]);

  useEffect(() => {
    if (user && user.status === 'pending') void navigate({ to: '/pending-approval' });
  }, [user, navigate]);

  if (loading) {
    return (
      <main className="rk-shell" style={{ paddingTop: 'var(--rk-space-11)' }}>
        <Skeleton height={27} width="32%" />
        <Skeleton height={12} width="20%" style={{ marginTop: 'var(--rk-space-5)' }} />
      </main>
    );
  }
  if (!user) return <AnonymousJobsPage />;
  if (user.status === 'pending') return null;
  return <JobsListPage />;
}

/** Cancel / delete (with their confirmations), download and copy-link, shared by both lists. */
function useJobRowActions(onChanged: () => void) {
  const { toast } = useToast();
  const [confirm, setConfirm] = useState<{ kind: 'cancel' | 'delete'; job: Job } | null>(null);

  const cancel = useMutation({
    mutationFn: (job: Job) => api.cancelJob(job.id),
    onSuccess: () => onChanged(),
    onError: (err) => toast({ kind: 'error', title: 'Could not cancel the job', detail: errorMessage(err) }),
  });
  const del = useMutation({
    mutationFn: (job: Job) => api.deleteJob(job.id),
    onSuccess: (_r, job) => {
      forgetClaim(job.id);
      onChanged();
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

  const row = (job: Job) => ({
    busy: (cancel.isPending && cancel.variables?.id === job.id) || (del.isPending && del.variables?.id === job.id),
    onCancel: (j: Job) => setConfirm({ kind: 'cancel', job: j }),
    onDelete: (j: Job) => setConfirm({ kind: 'delete', job: j }),
    onDownload: (j: Job) => void download(j),
    onCopyLink: (j: Job) => void copyLink(j),
  });

  const dialogs: ReactNode = (
    <>
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
    </>
  );

  return { row, dialogs };
}

function JobsListPage() {
  const navigate = useNavigate();
  const go = useAppNavigate();
  const search = jobsRoute.useSearch();
  const qc = useQueryClient();
  const status: JobListStatus = search.status ?? 'all';
  const page = search.page ?? 1;
  const [q, setQ] = useState(search.q ?? '');
  const debouncedQ = useDebounced(q, 250);

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

  const actions = useJobRowActions(() => void qc.invalidateQueries({ queryKey: ['jobs'] }));

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
      <div className="rk-pagehead-row">
        <div>
          <h1 className="rk-title">Job History</h1>
          <p className="rk-help" style={{ margin: 'var(--rk-space-3) 0 0' }}>
            {subtitle}
          </p>
        </div>
        <button className="rk-btn rk-btn--primary" type="button" onClick={() => go({ to: '/' })}>
          <Icon name="plus" /> New job
        </button>
      </div>

      {!jobs.isPending && totalAll === 0 && !filtered ? (
        <EmptyState
          title="No jobs yet"
          action={
            <button className="rk-btn rk-btn--primary" type="button" onClick={() => go({ to: '/' })}>
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
                  <JobRow key={job.id} job={job} {...actions.row(job)} />
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

      {actions.dialogs}
    </main>
  );
}

/** Statuses that mean the token no longer opens the job: gone, expired, or claimed by an account. */
const CLAIM_DEAD = new Set([401, 403, 404, 410]);

/**
 * Signed-out /jobs: the jobs whose claim tokens live in localStorage
 * (rk.claims), each fetched with X-Claim-Token. Tokens that no longer work
 * are forgotten. Signing in claims them (AuthProvider.onSignedIn).
 */
function AnonymousJobsPage() {
  const { openSignIn } = useAuth();
  const go = useAppNavigate();
  const qc = useQueryClient();
  const config = useQuery({ queryKey: ['config'], queryFn: api.getConfig, staleTime: Infinity });
  const anonHours = config.data?.anon_retention_hours ?? 24;
  const [claims, setClaims] = useState(() => allClaims());
  const ids = useMemo(() => Object.keys(claims), [claims]);

  const results = useQueries({
    queries: ids.map((id) => ({
      queryKey: ['jobs', 'anon', id],
      queryFn: () => api.getJob(id),
      retry: (n: number, err: unknown) => !(err instanceof ApiError) && n < 2,
      refetchInterval: (query: { state: { data?: Job } }) => (query.state.data && isActive(query.state.data.status) ? 3000 : false),
      refetchIntervalInBackground: false,
    })),
  });

  // Drop tokens the server no longer honours.
  const dead = results.map((r, i) => (r.error instanceof ApiError && CLAIM_DEAD.has(r.error.status) ? ids[i] : null)).filter((x): x is string => x !== null);
  useEffect(() => {
    if (dead.length === 0) return;
    dead.forEach((id) => forgetClaim(id));
    setClaims(allClaims());
  }, [dead.join(',')]); // eslint-disable-line react-hooks/exhaustive-deps

  const actions = useJobRowActions(() => {
    setClaims(allClaims());
    void qc.invalidateQueries({ queryKey: ['jobs', 'anon'] });
  });

  const pending = results.some((r) => r.isPending);
  const failed = results.filter((r) => r.error && !(r.error instanceof ApiError && CLAIM_DEAD.has(r.error.status)));
  const items = results
    .map((r) => r.data)
    .filter((j): j is Job => Boolean(j))
    .sort((a, b) => (a.created_at < b.created_at ? 1 : -1));
  const runningCount = items.filter((j) => isActive(j.status)).length;

  const subtitle = pending
    ? 'Loading jobs…'
    : items.length === 0
      ? 'No anonymous jobs on this device'
      : `${items.length} anonymous job${items.length === 1 ? '' : 's'} on this device${runningCount ? ` · ${runningCount} running` : ''}`;

  return (
    <main className="rk-shell" style={{ paddingTop: 'var(--rk-space-11)', position: 'relative' }} data-testid="anon-jobs">
      <div className="rk-pagehead-row">
        <div>
          <h1 className="rk-title">Job History</h1>
          <p className="rk-help" style={{ margin: 'var(--rk-space-3) 0 0' }}>
            {subtitle}
          </p>
        </div>
        <div style={{ display: 'flex', gap: 'var(--rk-space-5)', flexWrap: 'wrap' }}>
          <button className="rk-btn" type="button" onClick={() => go({ to: '/' })}>
            <Icon name="plus" /> New job
          </button>
          <button className="rk-btn rk-btn--primary" type="button" onClick={() => openSignIn()} data-testid="jobs-sign-in">
            Sign in
          </button>
        </div>
      </div>

      <div className="rk-alert rk-alert--warn" style={{ marginBottom: 'var(--rk-space-9)' }} data-testid="anon-banner">
        <Icon name="alert" size={18} />
        <div>
          <strong>You are not signed in.</strong> Anonymous jobs are remembered by this browser only and are removed {anonHours} hours after they were created — clearing site data loses the links.{' '}
          <a href="#" onClick={(e) => { e.preventDefault(); openSignIn(); }}>
            Sign in
          </a>{' '}
          to attach them to your account and keep them.
        </div>
      </div>

      <h2 className="rk-section-title">Your anonymous jobs on this device</h2>

      {ids.length === 0 ? (
        <EmptyState
          title="No anonymous jobs on this device"
          action={
            <button className="rk-btn rk-btn--primary" type="button" onClick={() => go({ to: '/' })}>
              Upload a track
            </button>
          }
        >
          Jobs you start without signing in show up here for {anonHours} hours, as long as you use this browser. Sign in to keep a history that follows you.
        </EmptyState>
      ) : pending && items.length === 0 ? (
        <div className="rk-joblist" aria-busy="true">
          {ids.slice(0, 5).map((id) => (
            <JobRowSkeleton key={id} />
          ))}
        </div>
      ) : (
        <>
          {failed.length > 0 && (
            <div className="rk-alert" role="alert" style={{ marginBottom: 'var(--rk-space-7)' }}>
              <Icon name="alert" size={18} />
              <div>
                {failed.length === 1 ? 'One job' : `${failed.length} jobs`} could not be loaded — {errorMessage(failed[0].error)}.{' '}
                <a href="#" onClick={(e) => { e.preventDefault(); failed.forEach((r) => void r.refetch()); }}>
                  Try again
                </a>
                .
              </div>
            </div>
          )}
          <div className="rk-joblist">
            {items.map((job) => (
              <JobRow key={job.id} job={job} {...actions.row(job)} />
            ))}
          </div>
        </>
      )}

      {actions.dialogs}
    </main>
  );
}

function numberWord(n: number): string {
  const words = ['zero', 'one', 'two', 'three', 'four', 'five', 'six'];
  return words[n] ?? String(n);
}
