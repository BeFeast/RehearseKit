import type { YouTubePreview } from '../../api/types';
import { formatTimecode, shortUrl } from '../../lib/format';

export function isYouTubeUrl(raw: string): boolean {
  try {
    const u = new URL(raw);
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return false;
    const host = u.hostname.toLowerCase();
    return ['youtube.com', 'www.youtube.com', 'm.youtube.com', 'music.youtube.com', 'youtu.be'].includes(host);
  } catch {
    return false;
  }
}

export type UrlState =
  | { kind: 'fetching' }
  | { kind: 'ready'; preview: YouTubePreview }
  | { kind: 'unavailable'; reason: string }
  | { kind: 'confirmed'; preview: YouTubePreview | null };

/**
 * components/url-preview-card: fetching skeleton → ready card with
 * Use this video / Not this one → confirmed compact row. When the preview
 * route is missing (404/501) the card says so and still lets the visitor
 * continue with the bare URL.
 */
export function UrlPreviewCard({ state, url, onUse, onReject }: { state: UrlState; url: string; onUse(): void; onReject(): void }) {
  if (state.kind === 'fetching') {
    return (
      <div className="rk-filecard" aria-busy="true">
        <div className="rk-thumb rk-skel" />
        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-4)' }}>
          <div className="rk-skel" style={{ height: 14, width: '62%' }} />
          <div className="rk-skel" style={{ height: 11, width: '36%' }} />
        </div>
      </div>
    );
  }
  if (state.kind === 'unavailable') {
    return (
      <div className="rk-filecard" role="status" aria-live="polite" data-testid="url-unavailable">
        <div className="rk-thumb">NO PREVIEW</div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontWeight: 600 }}>Preview unavailable</div>
          <div className="rk-mono rk-muted" style={{ fontSize: 'var(--rk-font-size-md)', marginTop: 'var(--rk-space-2)' }}>
            {state.reason} · {shortUrl(url)}
          </div>
          <div style={{ display: 'flex', gap: 'var(--rk-space-5)', marginTop: 'var(--rk-space-5)' }}>
            <button className="rk-btn rk-btn--sm" type="button" onClick={onUse}>
              Continue anyway
            </button>
            <button className="rk-btn rk-btn--sm" type="button" onClick={onReject}>
              Not this one
            </button>
          </div>
        </div>
      </div>
    );
  }
  const preview = state.preview;
  const meta = preview
    ? [preview.duration_seconds != null ? formatTimecode(preview.duration_seconds) : null, preview.channel, hostOf(preview.url || url)].filter(Boolean).join(' · ')
    : shortUrl(url);
  return (
    <div className="rk-filecard" role="status" aria-live="polite" data-testid="url-preview">
      <div className="rk-thumb">
        {preview?.thumbnail_url ? <img src={preview.thumbnail_url} alt="" style={{ width: '100%', height: '100%', objectFit: 'cover' }} /> : 'THUMBNAIL'}
      </div>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontWeight: 600, display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical', overflow: 'hidden' }}>{preview?.title ?? shortUrl(url)}</div>
        <div className="rk-mono rk-muted" style={{ fontSize: 'var(--rk-font-size-md)', marginTop: 'var(--rk-space-2)' }}>
          {state.kind === 'confirmed' ? `${meta} · selected` : meta}
        </div>
        {state.kind === 'ready' && (
          <div style={{ display: 'flex', gap: 'var(--rk-space-5)', marginTop: 'var(--rk-space-5)' }}>
            <button className="rk-btn rk-btn--sm" type="button" onClick={onUse}>
              Use this video
            </button>
            <button className="rk-btn rk-btn--sm" type="button" onClick={onReject}>
              Not this one
            </button>
          </div>
        )}
        {state.kind === 'confirmed' && (
          <div style={{ display: 'flex', gap: 'var(--rk-space-5)', marginTop: 'var(--rk-space-5)' }}>
            <button className="rk-btn rk-btn--sm" type="button" onClick={onReject}>
              Change video
            </button>
          </div>
        )}
      </div>
    </div>
  );
}

function hostOf(url: string): string {
  try {
    return new URL(url).hostname.replace(/^www\./, '');
  } catch {
    return 'youtube.com';
  }
}
