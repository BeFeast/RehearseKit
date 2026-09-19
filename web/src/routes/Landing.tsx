import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useAppNavigate } from '../lib/use-app-navigate';
import { useQuery } from '@tanstack/react-query';
import * as api from '../api';
import { ApiError, errorMessage } from '../api/client';
import type { Quality, YouTubePreview } from '../api/types';
import { useAuth } from '../auth/AuthProvider';
import { Progress } from '../components/Badge';
import { Icon } from '../components/Icon';
import { Panel } from '../components/Panel';
import { useToast } from '../components/Toast';
import { AudioPreview, describeFile, validateFile, type FileInfo } from '../components/upload/AudioPreview';
import { Dropzone } from '../components/upload/Dropzone';
import { UrlPreviewCard, isYouTubeUrl } from '../components/upload/UrlPreviewCard';
import { rememberClaim } from '../lib/claim-tokens';
import { formatBytes, formatEta } from '../lib/format';

type Mode = 'file' | 'url';

const QUALITY_HELP: Record<Quality, string> = {
  fast: 'Demucs standard — about 1 minute per song minute.',
  high: 'Demucs HT — cleaner separation, about 3 minutes per song minute.',
  high6: 'Demucs HT 6-source — adds guitar and piano stems, about 4 minutes per song minute.',
};

// Three segments still fit the control while labels stay under ~14 characters (README decision).
const QUALITY_LABEL: Record<Quality, string> = { fast: 'FAST', high: 'HIGH QUALITY', high6: 'HQ \u00b7 6 STEMS' };

const ONE_GB = 1024 * 1024 * 1024;

/** screens/03-landing-upload: one form, two modes, feature blurbs beneath. */
export function LandingRoute() {
  const navigate = useAppNavigate();
  const { user, openSignIn } = useAuth();
  const { toast } = useToast();
  const config = useQuery({ queryKey: ['config'], queryFn: api.getConfig, staleTime: Infinity });
  const maxBytes = config.data?.max_upload_bytes ?? ONE_GB;
  const anonHours = config.data?.anon_retention_hours ?? 24;

  const [mode, setMode] = useState<Mode>('file');
  const [file, setFile] = useState<File | null>(null);
  const [fileInfo, setFileInfo] = useState<FileInfo | null>(null);
  const [fileError, setFileError] = useState<string | null>(null);
  const [url, setUrl] = useState('');
  const [urlState, setUrlState] = useState<{ kind: 'idle' } | { kind: 'fetching' } | { kind: 'ready'; preview: YouTubePreview } | { kind: 'unavailable'; reason: string } | { kind: 'confirmed'; preview: YouTubePreview | null }>({ kind: 'idle' });
  const [urlError, setUrlError] = useState<string | null>(null);
  const [name, setName] = useState('');
  const [nameTouched, setNameTouched] = useState(false);
  const [quality, setQuality] = useState<Quality>('high');
  const [upload, setUpload] = useState<{ loaded: number; total: number; startedAt: number } | null>(null);
  const [creating, setCreating] = useState(false);
  const abortRef = useRef<(() => void) | null>(null);
  const urlInput = useRef<HTMLInputElement>(null);

  useEffect(() => {
    document.title = 'Upload — RehearseKit';
  }, []);

  const chooseFile = useCallback(
    (f: File) => {
      const err = validateFile(f, maxBytes);
      if (err) {
        setFile(null);
        setFileInfo(null);
        setFileError(err);
        return;
      }
      setFileError(null);
      setFile(f);
      setFileInfo(describeFile(f));
      if (!nameTouched) setName(f.name.replace(/\.[^.]+$/, ''));
    },
    [maxBytes, nameTouched],
  );

  const removeFile = () => {
    setFile(null);
    setFileInfo(null);
    setFileError(null);
  };

  const switchMode = (m: Mode) => {
    if (m === mode) return;
    const otherChosen = m === 'file' ? urlState.kind === 'ready' || urlState.kind === 'confirmed' : file !== null;
    if (otherChosen && !window.confirm(m === 'file' ? 'Switch to a file upload? The fetched video is cleared.' : 'Switch to a YouTube link? The chosen file is cleared.')) return;
    if (m === 'file') {
      setUrlState({ kind: 'idle' });
      setUrlError(null);
    } else {
      removeFile();
    }
    setMode(m);
  };

  const fetchPreview = async () => {
    const trimmed = url.trim();
    if (!isYouTubeUrl(trimmed)) {
      setUrlError('That is not a YouTube link. Paste a youtube.com or youtu.be URL.');
      return;
    }
    setUrlError(null);
    setUrlState({ kind: 'fetching' });
    try {
      const preview = await api.youtubePreview(trimmed);
      setUrlState({ kind: 'ready', preview });
      if (!nameTouched && preview.title) setName(preview.title);
    } catch (err) {
      if (err instanceof ApiError && err.unavailable) {
        setUrlState({ kind: 'unavailable', reason: 'Video preview is not available in this build.' });
      } else if (err instanceof ApiError && err.status >= 400 && err.status < 500) {
        setUrlState({ kind: 'idle' });
        setUrlError(err.message);
      } else {
        setUrlState({ kind: 'unavailable', reason: errorMessage(err, 'Could not read the video page.') });
      }
    }
  };

  const hasSource = mode === 'file' ? file !== null : urlState.kind === 'confirmed';

  const submit = async () => {
    if (!hasSource || creating) return;
    setCreating(true);
    const input: api.CreateJobInput = { quality, project_name: name.trim() || undefined };
    if (mode === 'file' && file) input.file = file;
    else input.input_url = url.trim();
    const started = Date.now();
    if (input.file) setUpload({ loaded: 0, total: input.file.size, startedAt: started });
    const { promise, abort } = api.createJob(input, (p) => setUpload({ loaded: p.loaded, total: p.total, startedAt: started }));
    abortRef.current = abort;
    try {
      const job = await promise;
      if (job.claim_token) rememberClaim(job.id, job.claim_token);
      void navigate({ to: '/jobs/$id', params: { id: job.id } });
    } catch (err) {
      if (err instanceof ApiError && err.code === 'aborted') {
        toast({ kind: 'info', title: 'Upload cancelled', detail: 'No job was created.' });
      } else if (err instanceof ApiError && err.code === 'too_large') {
        setFileError(`${file?.name ?? 'The file'} is ${formatBytes(file?.size ?? 0)}. RehearseKit accepts files up to ${formatBytes(maxBytes)}.`);
        removeFileKeepError();
      } else {
        toast({ kind: 'error', title: 'Could not create the job', detail: errorMessage(err) });
      }
    } finally {
      abortRef.current = null;
      setUpload(null);
      setCreating(false);
    }
  };
  const removeFileKeepError = () => {
    setFile(null);
    setFileInfo(null);
  };

  const eta = useMemo(() => {
    if (!upload) return '';
    const elapsed = (Date.now() - upload.startedAt) / 1000;
    if (elapsed < 3 || upload.loaded === 0) return '';
    const rate = upload.loaded / elapsed;
    return formatEta((upload.total - upload.loaded) / rate);
  }, [upload]);

  const anonymous = !user && !config.isPending;

  return (
    <main className="rk-shell" style={{ paddingTop: 'var(--rk-space-11)' }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-5)', marginBottom: 'var(--rk-space-11)', maxWidth: '60ch' }}>
        <h1 className="rk-title">Separate any track into stems</h1>
        <p style={{ margin: 0, fontSize: 'var(--rk-font-size-xl)', color: 'var(--rk-color-ink-muted)' }}>
          Upload a recording or paste a YouTube link. RehearseKit returns vocals, drums, bass and everything else, plus a DAWproject file and a tempo map.
        </p>
      </div>

      {anonymous && (
        <div className="rk-alert rk-alert--warn" style={{ marginBottom: 'var(--rk-space-8)' }} data-testid="anon-banner">
          <Icon name="alert" size={18} />
          <div>
            <strong>You are not signed in.</strong> Anonymous jobs run normally and stay available for {anonHours} hours through the link you land on. They do not appear in Job History, and they cannot be recovered if you lose the link.{' '}
            <a href="#" onClick={(e) => { e.preventDefault(); openSignIn(); }}>
              Sign in
            </a>{' '}
            to keep them.
          </div>
        </div>
      )}

      {upload && file ? (
        <Panel style={{ padding: 'var(--rk-space-10)' }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-8)' }}>
            <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between' }}>
              <span className="rk-eyebrow">UPLOADING</span>
              <span className="rk-mono rk-muted" style={{ fontSize: 'var(--rk-font-size-md)' }}>
                {formatBytes(upload.loaded)} of {formatBytes(upload.total)}
                {eta ? ` · ${eta}` : ''}
              </span>
            </div>
            <FileCard file={file} info={fileInfo} onRemove={() => abortRef.current?.()} />
            <Progress value={upload.total ? (upload.loaded / upload.total) * 100 : 0} label="Upload progress" />
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 'var(--rk-space-8)' }}>
              <span className="rk-help">The job is created as soon as the upload completes — you will land on its page and can leave it running.</span>
              <button className="rk-btn" type="button" onClick={() => abortRef.current?.()}>
                Cancel upload
              </button>
            </div>
          </div>
        </Panel>
      ) : (
        <Panel style={{ padding: 'var(--rk-space-10) var(--rk-space-10) var(--rk-space-11)' }}>
          <div className="rk-tabs" role="tablist" style={{ marginBottom: 'var(--rk-space-9)' }}>
            <button className="rk-tab" role="tab" type="button" aria-selected={mode === 'file'} onClick={() => switchMode('file')}>
              Upload a file
            </button>
            <button className="rk-tab" role="tab" type="button" aria-selected={mode === 'url'} onClick={() => switchMode('url')}>
              YouTube link
            </button>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-9)' }} aria-busy={creating || undefined}>
            {mode === 'file' ? (
              file ? (
                <FileCard file={file} info={fileInfo} onRemove={removeFile} />
              ) : (
                <Dropzone onFile={chooseFile} error={fileError} onClearError={() => setFileError(null)} maxBytes={maxBytes} />
              )
            ) : (
              <div className="rk-field">
                <label htmlFor="yt">YouTube URL</label>
                <form
                  style={{ display: 'flex', gap: 'var(--rk-space-5)' }}
                  onSubmit={(e) => {
                    e.preventDefault();
                    void fetchPreview();
                  }}
                >
                  <input
                    ref={urlInput}
                    className="rk-input rk-input--lg"
                    id="yt"
                    type="url"
                    placeholder="https://www.youtube.com/watch?v=..."
                    value={url}
                    onChange={(e) => {
                      setUrl(e.target.value);
                      setUrlError(null);
                      if (urlState.kind !== 'idle' && urlState.kind !== 'fetching') setUrlState({ kind: 'idle' });
                    }}
                    aria-invalid={urlError ? true : undefined}
                    aria-describedby={urlError ? 'yt-err' : undefined}
                  />
                  <button className="rk-btn rk-btn--lg" type="submit" disabled={!isYouTubeUrl(url.trim()) || urlState.kind === 'fetching'}>
                    {urlState.kind === 'fetching' ? 'Fetching…' : 'Fetch'}
                  </button>
                </form>
                {urlError ? (
                  <span className="rk-help rk-help--error" id="yt-err" role="alert">
                    {urlError}
                  </span>
                ) : (
                  <span className="rk-help">Public videos only. Audio is downloaded at the best available quality.</span>
                )}
              </div>
            )}

            {mode === 'url' && urlState.kind !== 'idle' && (
              <UrlPreviewCard
                state={urlState}
                url={url.trim()}
                onUse={() => setUrlState({ kind: 'confirmed', preview: urlState.kind === 'ready' ? urlState.preview : null })}
                onReject={() => {
                  setUrlState({ kind: 'idle' });
                  urlInput.current?.focus();
                  urlInput.current?.select();
                }}
              />
            )}

            {/* Two columns, labels on one row, input and segmented control on one baseline (same height); the help lines sit under the form, left, with the button bottom-right on the last help line. */}
            <div className="rk-formgrid">
              <div className="rk-field">
                <label htmlFor="pname">Project name</label>
                <input
                  className="rk-input"
                  id="pname"
                  placeholder="My Awesome Song"
                  value={name}
                  onChange={(e) => {
                    setName(e.target.value);
                    setNameTouched(true);
                  }}
                  maxLength={200}
                />
              </div>
              <div className="rk-field">
                <label id="qlabel">Processing quality</label>
                <div className="rk-seg rk-seg--field" role="group" aria-labelledby="qlabel" aria-describedby="qhelp">
                  {(['fast', 'high', 'high6'] as Quality[]).map((q) => (
                    <button key={q} type="button" aria-pressed={quality === q} onClick={() => setQuality(q)}>
                      {QUALITY_LABEL[q]}
                    </button>
                  ))}
                </div>
              </div>
            </div>

            {mode === 'file' && file && <AudioPreview file={file} info={fileInfo} />}

            <div className="rk-formfoot">
              <div className="rk-formfoot-help">
                <span className="rk-help" id="qhelp" data-testid="quality-help">
                  {QUALITY_HELP[quality]}
                </span>
                <span className="rk-help">
                  {mode === 'url'
                    ? urlState.kind === 'fetching'
                      ? 'Reading the video page. This usually takes a second or two.'
                      : 'Only download audio you have the right to use.'
                    : fileError
                      ? 'The file never left your machine — type and size are checked before upload starts.'
                      : `WAV, MP3, FLAC and AIFF up to ${formatBytes(maxBytes)}. Lossless is recommended — separation quality follows the source.`}
                </span>
              </div>
              <button className="rk-btn rk-btn--primary rk-btn--lg" type="button" disabled={!hasSource || creating} aria-busy={creating || undefined} onClick={() => void submit()} data-testid="submit-job">
                {creating ? 'Creating job…' : anonymous ? 'Separate stems anonymously' : 'Separate stems'}
              </button>
            </div>
          </div>
        </Panel>
      )}

      <section className="rk-section" style={{ marginTop: 'var(--rk-space-13)' }}>
        <div className="rk-features" style={{ display: 'grid', gridTemplateColumns: 'repeat(3,minmax(0,1fr))', gap: 'var(--rk-space-6)' }}>
          <div className="rk-feature">
            <Icon name="solo" size={20} />
            <h4>Stem Separation</h4>
            <p>Vocals, drums, bass and other, separated with Demucs and delivered as individual WAV files.</p>
          </div>
          <div className="rk-feature">
            <Icon name="metronome" size={20} />
            <h4>Tempo Detection</h4>
            <p>Detected BPM and a beat map, embedded in every stem so your DAW lines up on import.</p>
          </div>
          <div className="rk-feature">
            <Icon name="folder" size={20} />
            <h4>DAW Integration</h4>
            <p>A DAWproject file alongside the stems: open the session in Bitwig, Studio One or Reaper with tracks already laid out.</p>
          </div>
        </div>
      </section>
    </main>
  );
}

function FileCard({ file, info, onRemove }: { file: File; info: FileInfo | null; onRemove: () => void }) {
  return (
    <div className="rk-filecard" data-testid="file-card">
      <span className="rk-filecard-icon">
        <Icon name="file-audio" size={20} />
      </span>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{file.name}</div>
        <div className="rk-mono rk-muted" style={{ fontSize: 'var(--rk-font-size-md)', marginTop: 'var(--rk-space-2)' }}>
          {info?.line ?? formatBytes(file.size)}
        </div>
      </div>
      <button className="rk-iconbtn" type="button" aria-label="Remove file" onClick={onRemove}>
        <Icon name="x" />
      </button>
    </div>
  );
}
