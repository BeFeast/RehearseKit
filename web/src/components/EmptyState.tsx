import type { ReactNode } from 'react';
import emptyJobs from '../assets/illustrations/empty-jobs.svg';
import errorArt from '../assets/illustrations/error.svg';
import processingArt from '../assets/illustrations/processing.svg';

export const ILLUSTRATIONS = { 'empty-jobs': emptyJobs, error: errorArt, processing: processingArt } as const;

export function EmptyState({
  art = 'empty-jobs',
  title,
  children,
  action,
}: {
  art?: keyof typeof ILLUSTRATIONS;
  title: string;
  children?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="rk-empty">
      <img src={ILLUSTRATIONS[art]} alt="" width={132} height={96} />
      <h3>{title}</h3>
      {children && <p>{children}</p>}
      {action}
    </div>
  );
}

/** The centred panel body used by the failed / cancelled / processing job states. */
export function PanelNotice({
  art,
  title,
  children,
  actions,
  maxWidth = '46ch',
}: {
  art: keyof typeof ILLUSTRATIONS;
  title: string;
  children?: ReactNode;
  actions?: ReactNode;
  maxWidth?: string;
}) {
  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        gap: 'var(--rk-space-6)',
        padding: 'var(--rk-space-13) var(--rk-space-8)',
        textAlign: 'center',
      }}
    >
      <img src={ILLUSTRATIONS[art]} alt="" width={132} height={96} />
      <div style={{ fontSize: 'var(--rk-font-size-xl)', fontWeight: 'var(--rk-font-weight-semibold)' as never }}>{title}</div>
      {children && (
        <p style={{ margin: 0, maxWidth, fontSize: 'var(--rk-font-size-base)', color: 'var(--rk-color-ink-muted)' }}>{children}</p>
      )}
      {actions && <div style={{ display: 'flex', gap: 'var(--rk-space-5)', flexWrap: 'wrap', justifyContent: 'center' }}>{actions}</div>}
    </div>
  );
}
