import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import * as api from '../api';
import { ApiError, errorMessage } from '../api/client';
import { subscribeJobEvents } from '../api/sse';
import type { Job, JobEvent, JobStatus } from '../api/types';
import { useAuth } from '../auth/AuthProvider';
import { Badge, Progress, QualityBadge, Skeleton, StatusBadge } from '../components/Badge';
import { AppLink } from '../components/AppLink';
import { ConfirmDialog } from '../components/Dialog';
import { PanelNotice } from '../components/EmptyState';
import { Icon } from '../components/Icon';
import { Divider, Panel } from '../components/Panel';
import { useToast } from '../components/Toast';
import { MasterStrip } from '../components/mixer/MasterStrip';
import { MobilePlayer } from '../components/mixer/MobilePlayer';
import { Strip } from '../components/mixer/Strip';
import { Transport } from '../components/mixer/Transport';
import { Waveform } from '../components/mixer/Waveform';
import { formatLoopSummary, formatRelative, hoursUntil } from '../lib/format';
import { isSilenced } from '../lib/mix-state';
import { canCancel, isActive, LAMPS, lampStates, overallProgress, STAGE_COPY, stageNoun, stemModelCaption } from '../lib/stages';
import { useAppNavigate } from '../lib/use-app-navigate';
import { useMixer, type Mixer } from '../player/use-mixer';
import { jobRoute } from '../router';
import { NotFoundScreen } from './Errors';

const JOB_KEY = (id: string) => ['jobs', 'detail', id] as const;

/** /jobs/$id — one route for processing, completed, failed and cancelled. */
export function JobDetailRoute() {
  const { id } = jobRoute.useParams();
  const { loading: authLoading, openSignIn } = useAuth();
  const qc = useQueryClient();

  const job = useQuery({
    queryKey: JOB_KEY(id),
    queryFn: () => api.getJob(id),
    retry: (n, err) => !(err instanceof ApiError) && n < 3,
    enabled: !authLoading,
  });

  // Live stage updates through SSE while the job is active.
  const status = job.data?.status;
  const [lastEvent, setLastEvent] = useState<JobEvent | null>(null);
  const [frozenAt, setFrozenAt] = useState<{ status: JobStatus; progress: number } | null>(null);
  useEffect(() => {
    if (!status) return;
    setLastEvent(null);
    let lastLive: { status: JobStatus; progress: number } | null = null;
    const sub = subscribeJobEvents(
      id,
      (ev) => {
        if (isActive(ev.status)) lastLive = { status: ev.status, progress: ev.progress };
        setLastEvent(ev);
        qc.setQueryData<Job>(JOB_KEY(id), (cur) => {
          if (!cur) return cur;
          if (ev.status === cur.status && ev.progress < cur.stage_progress) return cur; // never move backwards
          return { ...cur, status: ev.status, stage_progress: ev.progress, error: ev.status === 'failed' ? ev.message || cur.error : cur.error };
        });
        if (ev.status === 'completed' || ev.status === 'failed' || ev.status === 'cancelled') {
          setFrozenAt(lastLive);
          void qc.invalidateQueries({ queryKey: JOB_KEY(id) });
          void qc.invalidateQueries({ queryKey: ['jobs', 'list'] });
        }
      },
      () => undefined,
    );
    return () => sub.close();
    // Re-subscribe only when the job changes identity or (re)enters activity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, status === undefined ? undefined : isActive(status) ? 'active' : status]);

  useEffect(() => {
    document.title = job.data ? `${job.data.project_name} — RehearseKit` : 'Job — RehearseKit';
  }, [job.data]);

  // 401: an owned job without a session. The page is cross-origin isolated
  // (COOP blocks the Google popup), so "Open sign in" hands off to
  // /jobs?signin=1&next=… (AuthProvider.openSignIn) instead of a dialog here.
  if (job.isPending || authLoading) return <LoadingSkeleton />;
  if (job.error) {
    const err = job.error;
    if (err instanceof ApiError && (err.status === 404 || err.status === 410)) return <NotFoundScreen />;
    if (err instanceof ApiError && err.status === 401) {
      return (
        <main className="rk-shell" style={{ paddingTop: 'var(--rk-space-11)', position: 'relative' }}>
          <h1 className="rk-title">This job belongs to an account</h1>
          <p className="rk-help" style={{ margin: 'var(--rk-space-3) 0 0' }}>
            Sign in to open it.{' '}
            <a href="#" onClick={(e) => { e.preventDefault(); openSignIn(() => void job.refetch()); }}>
              Open sign in
            </a>
            .
          </p>
        </main>
      );
    }
    if (err instanceof ApiError) throw err;
    // Network failure (proxy hiccup, offline): a retry notice beats the error boundary.
    return (
      <main className="rk-shell" style={{ paddingTop: 'var(--rk-space-11)', position: 'relative' }}>
        <PanelNotice
          art="error"
          title="Could not reach the server"
          maxWidth="52ch"
          actions={
            <button className="rk-btn rk-btn--primary" type="button" onClick={() => void job.refetch()}>
              <Icon name="refresh" size={18} /> Try again
            </button>
          }
        >
          {errorMessage(err, 'The request did not complete.')} Your job is not affected — it keeps processing on the server.
        </PanelNotice>
      </main>
    );
  }
  return <JobPage job={job.data} lastEvent={lastEvent} frozenAt={frozenAt} />;
}

function JobPage({ job, lastEvent, frozenAt }: { job: Job; lastEvent: JobEvent | null; frozenAt: { status: JobStatus; progress: number } | null }) {
  const navigate = useAppNavigate();
  const qc = useQueryClient();
  const { toast } = useToast();
  const { user, openSignIn } = useAuth();
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const cancel = useMutation({
    mutationFn: () => api.cancelJob(job.id),
    onSuccess: (j) => {
      qc.setQueryData(JOB_KEY(job.id), j);
      void qc.invalidateQueries({ queryKey: ['jobs', 'list'] });
    },
    onError: (err) => toast({ kind: 'error', title: 'Could not cancel the job', detail: errorMessage(err) }),
  });
  const del = useMutation({
    mutationFn: () => api.deleteJob(job.id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['jobs', 'list'] });
      toast({ kind: 'success', title: 'Job deleted' });
      void navigate({ to: user ? '/jobs' : '/' });
    },
    onError: (err) => toast({ kind: 'error', title: 'Could not delete the job', detail: errorMessage(err) }),
  });

  async function download() {
    if (await api.downloadAvailable(job.id)) {
      window.location.assign(api.downloadUrl(job.id));
      toast({ kind: 'info', title: 'Download started', detail: 'Stems, DAWproject file and tempo map.' });
    } else {
      toast({ kind: 'error', title: 'Package download is not available yet', detail: 'This build serves stems for playback only; the zip package lands with the worker.' });
    }
  }
  function reprocess() {
    toast({ kind: 'info', title: 'Reprocessing lands with the worker', detail: 'This build cannot requeue a job yet — upload the source again from the home page.' });
  }

  const pct = job.status === 'failed' || job.status === 'cancelled' ? overallProgress(frozenAt?.status ?? 'separating', frozenAt?.progress ?? job.stage_progress) : overallProgress(job.status, job.stage_progress);
  const lamps = lampStates(job.status, frozenAt?.status ?? null);
  const stage = STAGE_COPY[job.status as keyof typeof STAGE_COPY];
  const anonymous = job.owner_id === null;
  const completed = job.status === 'completed';

  return (
    <main className="rk-shell" style={{ position: 'relative' }}>
      <div className="rk-pagehead">
        <AppLink className="rk-back" to="/jobs" aria-label="Back to jobs">
          &larr;
        </AppLink>
        <div style={{ flex: 1, minWidth: 0 }}>
          <h1 className="rk-title">{job.project_name}</h1>
          <p className="rk-subtitle">Job ID: {job.id}</p>
        </div>
        <div className="rk-headactions">
          <StatusBadge status={job.status} />
          <QualityBadge quality={job.quality} />
          {job.transcribe && (
            <Badge tone="outline" title="Beat grid + MIDI in the package">
              TRANSCRIBE
            </Badge>
          )}
          <button className="rk-btn rk-btn--primary" type="button" disabled={!completed} onClick={() => void download()} data-testid="download">
            Download Package
          </button>
        </div>
      </div>

      {anonymous && (
        <div className="rk-alert rk-alert--warn" style={{ marginBottom: 'var(--rk-space-7)' }} data-testid="anon-banner">
          <Icon name="alert" size={18} />
          <div>
            <strong>This is an anonymous job.</strong> Anyone with this link can play and download it, and it is removed {hoursUntil(job.expires_at)} hours from now.{' '}
            {user ? (
              <>It could not be attached to your account — the claim token for it is not on this device.</>
            ) : (
              <>
                <a href="#" onClick={(e) => { e.preventDefault(); openSignIn(() => void qc.invalidateQueries({ queryKey: JOB_KEY(job.id) })); }}>
                  Sign in
                </a>{' '}
                to attach it to your account and keep it.
              </>
            )}
          </div>
        </div>
      )}

      <div className="rk-panel rk-panel--flat rk-pipeline">
        <div className="rk-pipeline-copy">
          <span className="rk-eyebrow">PROCESS</span>
          <span className="rk-pipeline-msg" aria-live="polite">
            {job.status === 'failed'
              ? `Failed at ${stageNoun(frozenAt?.status ?? null)}`
              : job.status === 'cancelled'
                ? `Cancelled at ${stageNoun(frozenAt?.status ?? null)}`
                : stage?.message}
          </span>
          <span className="rk-pipeline-detail">
            {job.status === 'failed'
              ? job.error || 'The worker reported an error — no stems were written'
              : job.status === 'cancelled'
                ? `You stopped this job ${formatRelative(job.completed_at ?? job.created_at)} — no stems were written`
                : lastEvent?.message && lastEvent.status === job.status && isActive(job.status)
                  ? lastEvent.message
                  : stage?.detail}
          </span>
        </div>
        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-4)' }}>
          <div className="rk-lamps">
            {LAMPS.map((l, i) => (
              <div className="rk-lamp" data-state={lamps[i]} key={l}>
                <i />
                <span>{l}</span>
              </div>
            ))}
          </div>
          {job.status === 'pending' ? <Progress indeterminate label="Queued" /> : <Progress value={pct} />}
        </div>
        <div className="rk-pipeline-tail" style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 'var(--rk-space-3)', minWidth: 96 }}>
          <span className="rk-readout-lg">{job.status === 'pending' ? '—' : `${pct}%`}</span>
          {canCancel(job.status) ? (
            <button className="rk-btn rk-btn--mono rk-btn--sm" type="button" onClick={() => setConfirmCancel(true)} disabled={cancel.isPending}>
              CANCEL JOB
            </button>
          ) : completed ? (
            <button className="rk-btn rk-btn--mono rk-btn--sm" type="button" onClick={reprocess}>
              {job.quality === 'fast' ? 'REPROCESS · HQ' : 'REPROCESS'}
            </button>
          ) : job.status === 'failed' ? (
            <button className="rk-btn rk-btn--mono rk-btn--sm" type="button" onClick={reprocess}>
              RETRY · STD
            </button>
          ) : job.status === 'cancelled' ? (
            <button className="rk-btn rk-btn--mono rk-btn--sm" type="button" onClick={reprocess}>
              START AGAIN
            </button>
          ) : null}
        </div>
      </div>

      {completed ? (
        <MixerPanel job={job} onDownload={() => void download()} />
      ) : (
        <Panel className="rk-panel-pad" style={{ padding: 'var(--rk-space-9) var(--rk-space-10) var(--rk-space-10)' }} screws={isActive(job.status)}>
          {isActive(job.status) ? (
            <>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-5)' }}>
                <span className="rk-eyebrow">SOURCE FILE — WAVEFORM</span>
                <div className="rk-wave" aria-label="Source waveform, stems not yet available" style={{ cursor: 'default' }}>
                  <svg viewBox="0 0 1000 118" preserveAspectRatio="none" aria-hidden="true" fill="var(--rk-color-wave-main)">
                    <rect x="0" y="58.5" width="1000" height="1" opacity=".35" />
                  </svg>
                  <div className="rk-wave-played" style={{ width: 0 }} />
                </div>
              </div>
              <Divider>STEM MIXER — UNAVAILABLE</Divider>
              <PanelNotice
                art="processing"
                title={job.status === 'pending' ? 'Waiting for a worker' : 'Stems are still being separated'}
                actions={
                  canCancel(job.status) ? (
                    <button className="rk-btn rk-btn--mono" type="button" onClick={() => setConfirmCancel(true)}>
                      CANCEL JOB
                    </button>
                  ) : undefined
                }
              >
                The mixer unlocks the moment Demucs finishes. Progress updates live — you can leave this page and come back.
              </PanelNotice>
            </>
          ) : job.status === 'failed' ? (
            <PanelNotice
              art="error"
              title={`This job stopped at ${stageNoun(frozenAt?.status ?? null)}`}
              maxWidth="52ch"
              actions={
                <>
                  <button className="rk-btn rk-btn--primary" type="button" onClick={reprocess}>
                    Retry at standard quality
                  </button>
                  <button className="rk-btn" type="button" onClick={() => void navigate({ to: user ? '/jobs' : '/' })}>
                    Back to jobs
                  </button>
                </>
              }
            >
              No stems were written, so nothing was charged against your quota. Retrying at standard quality uses roughly half the memory.
            </PanelNotice>
          ) : (
            <PanelNotice
              art="processing"
              title="This job was cancelled"
              maxWidth="52ch"
              actions={
                <>
                  <button className="rk-btn rk-btn--primary" type="button" onClick={reprocess}>
                    Start again
                  </button>
                  <button className="rk-btn" type="button" onClick={() => setConfirmDelete(true)}>
                    Delete job
                  </button>
                </>
              }
            >
              The source audio is still stored, so starting again costs nothing but time. Quality and project name are kept as they were.
            </PanelNotice>
          )}
        </Panel>
      )}

      <ConfirmDialog
        open={confirmCancel}
        title="Cancel this job?"
        body={`${job.project_name} is ${pct}% through processing. Cancelling discards the work done so far; the source file is kept so you can start it again.`}
        cancelLabel="Keep Processing"
        confirmLabel="Cancel Job"
        danger
        pending={cancel.isPending}
        onCancel={() => setConfirmCancel(false)}
        onConfirm={() => {
          setConfirmCancel(false);
          cancel.mutate();
        }}
      />
      <ConfirmDialog
        open={confirmDelete}
        title="Delete this job?"
        body={`${job.project_name} will be removed from storage. Anything you have already downloaded is unaffected. This cannot be undone.`}
        cancelLabel="Keep Job"
        confirmLabel="Delete Job"
        danger
        pending={del.isPending}
        onCancel={() => setConfirmDelete(false)}
        onConfirm={() => {
          setConfirmDelete(false);
          del.mutate();
        }}
      />
    </main>
  );
}

// ---- the mixer -------------------------------------------------------------

function MixerPanel({ job, onDownload }: { job: Job; onDownload(): void }) {
  const m = useMixer(job);
  const { toast } = useToast();
  useKeyboard(m);

  useEffect(() => {
    if (m.engineError) toast({ kind: 'error', title: 'Playback could not start', detail: m.engineError });
  }, [m.engineError, toast]);

  return (
    <>
      <div className="rk-only-desktop">
        <Panel className="rk-panel-pad" style={{ padding: 'var(--rk-space-9) var(--rk-space-10) var(--rk-space-10)' }}>
          <DesktopMixer m={m} />
        </Panel>
      </div>
      <div className="rk-only-mobile">
        <Panel style={{ padding: 'var(--rk-space-7) var(--rk-space-6) var(--rk-space-8)' }}>
          <MobilePlayer m={m} onDownload={onDownload} />
        </Panel>
      </div>
    </>
  );
}

function DesktopMixer({ m }: { m: Mixer }) {
  const live = m.live.current;
  void m.tick; // re-render at ~30 fps while audio moves
  const { mix } = m;
  const selectedName = mix.selected ? `${mix.selected.toUpperCase()} — STEM WAVEFORM` : 'MASTER MIX — WAVEFORM';
  const stem0 = m.job.stems[0];
  const soloLive = mix.solo !== null;
  const stemCount = m.stems.length;

  return (
    <>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-5)' }}>
        <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 'var(--rk-space-5)' }}>
          <span className="rk-eyebrow" data-testid="wave-title">
            {selectedName}
          </span>
          <span style={{ fontFamily: 'var(--rk-font-mono)', fontSize: 'var(--rk-font-size-sm)', color: 'var(--rk-color-ink-muted)' }}>
            {mix.loop ? formatLoopSummary(mix.loop.start, mix.loop.end) : m.peaksLoading ? 'LOADING WAVEFORM…' : 'NO LOOP — DRAG THE COPPER GRIPS OR PRESS L'}
          </span>
        </div>
        <Waveform
          peaks={m.peaks}
          stems={m.stems}
          selected={m.selected}
          duration={m.duration}
          position={live.position}
          loop={mix.loop}
          loopEnabled={mix.loopEnabled}
          bpm={m.bpm}
          onSeek={m.seek}
          onLoopChange={m.setLoop}
          label={selectedName}
        />
        <Transport
          playing={m.playing}
          starting={m.starting}
          position={live.position}
          duration={m.duration}
          bpm={m.bpm}
          loop={mix.loop}
          loopEnabled={mix.loopEnabled}
          onTogglePlay={m.togglePlay}
          onReturnToStart={m.returnToStart}
          onToggleLoop={m.toggleLoop}
        />
      </div>

      <Divider>STEM MIXER</Divider>
      <div className="rk-mixer" data-stems={stemCount} data-testid="mixer">
        {m.stems.map((stem, i) => {
          const s = mix.stems[stem];
          return (
            <Strip
              key={stem}
              stem={stem}
              index={i}
              caption={stemModelCaption(m.job.quality, stem)}
              position={s.position}
              muted={s.muted}
              silenced={isSilenced(mix, stem)}
              soloed={mix.solo === stem}
              selected={mix.selected === stem}
              level={live.levels[i] ?? 0}
              hold={live.holds[i] ?? 0}
              onPosition={(v) => m.dispatch({ type: 'gain', stem, position: v })}
              onToggleMute={() => m.toggleMute(stem)}
              onToggleSolo={() => m.toggleSolo(stem)}
              onSelect={() => m.dispatch({ type: 'select', stem })}
            />
          );
        })}
        <MasterStrip
          position={mix.masterPosition}
          bpm={m.bpm}
          bitDepth={stem0?.bit_depth ?? null}
          sampleRate={stem0?.sample_rate ?? m.job.sample_rate}
          left={live.master.left}
          right={live.master.right}
          leftHold={live.master.leftHold}
          rightHold={live.master.rightHold}
          soloLive={soloLive}
          selected={mix.selected === null}
          onPosition={(v) => m.dispatch({ type: 'master', position: v })}
          onClearSolo={m.clearSolo}
          onSelect={() => m.dispatch({ type: 'select', stem: null })}
        />
      </div>
      <div className="rk-hint">
        <p>
          Drag faders · Click a strip to show its waveform · <strong>S</strong> solo · <strong>M</strong> mute · drag the copper handles to set a rehearsal loop · <strong>Space</strong> play · <strong>L</strong> loop · <strong>1–{stemCount}</strong> solo
        </p>
        <p>Mix is preview only · download keeps original stems</p>
      </div>
    </>
  );
}

/** Page-level shortcuts (screens/02 notes): Space, Home, L, ←/→, 1–6. */
function useKeyboard(m: Mixer) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      if (!t) return;
      if (t.closest('input, textarea, select, [contenteditable="true"], [role="dialog"]')) return;
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      switch (e.key) {
        case ' ':
          e.preventDefault();
          m.togglePlay();
          return;
        case 'Home':
          e.preventDefault();
          m.returnToStart();
          return;
        case 'l':
        case 'L':
          e.preventDefault();
          m.toggleLoop();
          return;
        case 'ArrowLeft':
          e.preventDefault();
          m.nudge(e.shiftKey ? -1 : -5);
          return;
        case 'ArrowRight':
          e.preventDefault();
          m.nudge(e.shiftKey ? 1 : 5);
          return;
        default: {
          const n = Number(e.key);
          if (Number.isInteger(n) && n >= 1 && n <= m.stems.length) {
            e.preventDefault();
            m.toggleSolo(m.stems[n - 1]);
          }
        }
      }
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [m]);
}

// ---- loading skeleton (state-loading.html): same geometry, mixer dimmed ----

function LoadingSkeleton() {
  return (
    <main className="rk-shell" style={{ position: 'relative' }} aria-busy="true" data-testid="job-loading">
      <div className="rk-pagehead">
        <AppLink className="rk-back" to="/jobs" aria-label="Back to jobs">
          &larr;
        </AppLink>
        <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-5)' }}>
          <Skeleton height={27} width="44%" />
          <Skeleton height={12} width="28%" />
        </div>
        <div className="rk-headactions">
          <Skeleton height={26} width={96} />
          <Skeleton height={34} width={150} />
        </div>
      </div>
      <div className="rk-panel rk-panel--flat rk-pipeline">
        <div className="rk-pipeline-copy">
          <Skeleton height={9} width="60%" />
          <Skeleton height={14} width="90%" style={{ marginTop: 6 }} />
          <Skeleton height={11} width="70%" style={{ marginTop: 6 }} />
        </div>
        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-4)' }}>
          <Skeleton height={9} width="100%" />
          <Skeleton height={7} width="100%" />
        </div>
        <div style={{ minWidth: 96, display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 'var(--rk-space-3)' }}>
          <Skeleton height={15} width={60} />
          <Skeleton height={26} width={96} />
        </div>
      </div>
      <div className="rk-mixer-dim" aria-hidden="true">
        <Panel className="rk-panel-pad" style={{ padding: 'var(--rk-space-9) var(--rk-space-10) var(--rk-space-10)' }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-5)' }}>
            <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between' }}>
              <span className="rk-eyebrow">MASTER MIX — WAVEFORM</span>
            </div>
            <div className="rk-wave" />
            <div className="rk-transport">
              <button className="rk-tbtn" type="button" tabIndex={-1}>
                <Icon name="skip-start" size={18} />
              </button>
              <button className="rk-tbtn rk-tbtn--play" type="button" tabIndex={-1} />
              <button className="rk-tbtn rk-tbtn--toggle" type="button" tabIndex={-1}>
                LOOP
              </button>
              <div className="rk-spacer" />
              <div className="rk-lcd">
                <span className="rk-time">0:00</span>
                <span className="rk-dim">/ 0:00</span>
              </div>
            </div>
          </div>
          <Divider>STEM MIXER — LOADING</Divider>
          <div className="rk-mixer">
            {['vocals', 'drums', 'bass', 'other', 'master'].map((s) => (
              <section className={s === 'master' ? 'rk-strip rk-strip--master' : 'rk-strip'} key={s} style={{ minHeight: 360 }}>
                <div className="rk-strip-head">
                  <div className="rk-strip-led" style={{ background: 'var(--rk-color-led-off)' }} />
                  <div className="rk-strip-name">{s.toUpperCase()}</div>
                  <div className="rk-strip-sub">&nbsp;</div>
                </div>
              </section>
            ))}
          </div>
        </Panel>
      </div>
    </main>
  );
}

