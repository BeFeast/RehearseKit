import { useEffect, useId, useRef, type ReactNode } from 'react';

export interface DialogProps {
  open: boolean;
  title: ReactNode;
  children?: ReactNode;
  /** Right-aligned action row. */
  actions?: ReactNode;
  onClose: () => void;
  /** Destructive dialogs ignore scrim clicks (dialog spec). */
  dismissOnScrim?: boolean;
  /** Lock Esc/scrim while a request is in flight. */
  pending?: boolean;
  width?: number;
  labelledBy?: string;
  className?: string;
}

const FOCUSABLE = 'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';

/** Modal dialog: scrim at 42%, 420px raise panel, focus trapped, Esc closes. */
export function Dialog({ open, title, children, actions, onClose, dismissOnScrim = true, pending = false, width, className }: DialogProps) {
  const id = useId();
  const panel = useRef<HTMLDivElement>(null);
  const opener = useRef<Element | null>(null);

  useEffect(() => {
    if (!open) return;
    opener.current = document.activeElement;
    const el = panel.current;
    const first = el?.querySelector<HTMLElement>(FOCUSABLE);
    (first ?? el)?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !pending) {
        e.stopPropagation();
        onClose();
        return;
      }
      if (e.key === 'Tab' && el) {
        const nodes = Array.from(el.querySelectorAll<HTMLElement>(FOCUSABLE));
        if (nodes.length === 0) return;
        const firstNode = nodes[0];
        const last = nodes[nodes.length - 1];
        if (e.shiftKey && document.activeElement === firstNode) {
          e.preventDefault();
          last.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          firstNode.focus();
        }
      }
    };
    document.addEventListener('keydown', onKey, true);
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', onKey, true);
      document.body.style.overflow = prevOverflow;
      const o = opener.current;
      if (o instanceof HTMLElement) o.focus();
    };
  }, [open, onClose, pending]);

  if (!open) return null;
  return (
    <div
      className="rk-scrim rk-scrim--modal"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget && dismissOnScrim && !pending) onClose();
      }}
    >
      <div
        ref={panel}
        className={className ? `rk-dialog ${className}` : 'rk-dialog'}
        role="dialog"
        aria-modal="true"
        aria-labelledby={`${id}-title`}
        aria-busy={pending || undefined}
        tabIndex={-1}
        style={width ? { width } : undefined}
      >
        {typeof title === 'string' ? <h2 id={`${id}-title`}>{title}</h2> : <div id={`${id}-title`}>{title}</div>}
        {children}
        {actions && <div className="rk-dialog-actions">{actions}</div>}
      </div>
    </div>
  );
}

export interface ConfirmProps {
  open: boolean;
  title: string;
  body: ReactNode;
  cancelLabel: string;
  confirmLabel: string;
  danger?: boolean;
  pending?: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}

/** Confirmation with the app's own verbs (Keep Processing / Cancel Job …). */
export function ConfirmDialog(p: ConfirmProps) {
  return (
    <Dialog open={p.open} title={p.title} onClose={p.onCancel} dismissOnScrim={!p.danger} pending={p.pending}
      actions={
        <>
          <button className="rk-btn" type="button" onClick={p.onCancel} disabled={p.pending}>
            {p.cancelLabel}
          </button>
          <button
            className={p.danger ? 'rk-btn rk-btn--danger' : 'rk-btn rk-btn--primary'}
            type="button"
            onClick={p.onConfirm}
            disabled={p.pending}
            aria-busy={p.pending || undefined}
          >
            {p.confirmLabel}
          </button>
        </>
      }
    >
      <p>{p.body}</p>
    </Dialog>
  );
}
