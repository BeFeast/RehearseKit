import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from 'react';
import { Icon } from './Icon';

export type ToastKind = 'success' | 'error' | 'info';

export interface ToastInput {
  kind?: ToastKind;
  title: string;
  detail?: string;
  /** Milliseconds; errors persist until dismissed. */
  duration?: number;
  action?: { label: string; onClick: () => void };
}

interface ToastItem extends ToastInput {
  id: number;
  kind: ToastKind;
}

interface ToastApi {
  toast(input: ToastInput): number;
  dismiss(id: number): void;
}

const Ctx = createContext<ToastApi | null>(null);

const MAX_VISIBLE = 3;
const DEFAULT_MS = 6000;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const [queue, setQueue] = useState<ToastItem[]>([]);
  const seq = useRef(0);
  const timers = useRef(new Map<number, ReturnType<typeof setTimeout>>());

  const dismiss = useCallback((id: number) => {
    const t = timers.current.get(id);
    if (t) clearTimeout(t);
    timers.current.delete(id);
    setItems((cur) => cur.filter((i) => i.id !== id));
  }, []);

  const arm = useCallback(
    (item: ToastItem) => {
      if (item.kind === 'error') return;
      const ms = item.duration ?? DEFAULT_MS;
      timers.current.set(
        item.id,
        setTimeout(() => dismiss(item.id), ms),
      );
    },
    [dismiss],
  );

  const toast = useCallback(
    (input: ToastInput) => {
      const item: ToastItem = { ...input, id: ++seq.current, kind: input.kind ?? 'info' };
      setItems((cur) => {
        if (cur.length >= MAX_VISIBLE) {
          setQueue((q) => [...q, item]);
          return cur;
        }
        arm(item);
        return [...cur, item];
      });
      return item.id;
    },
    [arm],
  );

  // Promote queued toasts when a slot frees.
  if (items.length < MAX_VISIBLE && queue.length > 0) {
    const [next, ...rest] = queue;
    setQueue(rest);
    arm(next);
    setItems((cur) => [...cur, next]);
  }

  const pause = (id: number) => {
    const t = timers.current.get(id);
    if (t) {
      clearTimeout(t);
      timers.current.delete(id);
    }
  };
  const resume = (item: ToastItem) => {
    if (!timers.current.has(item.id)) arm(item);
  };

  const api = useMemo(() => ({ toast, dismiss }), [toast, dismiss]);

  return (
    <Ctx.Provider value={api}>
      {children}
      <div className="rk-toastwrap" role="region" aria-label="Notifications">
        {items.map((item) => (
          <div
            key={item.id}
            className={`rk-toast rk-toast--${item.kind}`}
            role={item.kind === 'error' ? 'alert' : 'status'}
            onMouseEnter={() => pause(item.id)}
            onMouseLeave={() => resume(item)}
            onFocus={() => pause(item.id)}
            onBlur={() => resume(item)}
          >
            <Icon name={item.kind === 'success' ? 'check' : item.kind === 'error' ? 'alert' : 'waveform'} size={18} />
            <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-2)' }}>
              <b style={{ fontSize: 'var(--rk-font-size-base)' }}>{item.title}</b>
              {item.detail && <span className="rk-help">{item.detail}</span>}
              {item.action && (
                <div>
                  <button
                    className="rk-btn rk-btn--sm"
                    type="button"
                    onClick={() => {
                      item.action?.onClick();
                      dismiss(item.id);
                    }}
                  >
                    {item.action.label}
                  </button>
                </div>
              )}
            </div>
            <button className="rk-iconbtn rk-toast-x" type="button" aria-label="Dismiss" onClick={() => dismiss(item.id)}>
              <Icon name="x" size={16} />
            </button>
          </div>
        ))}
      </div>
    </Ctx.Provider>
  );
}

export function useToast(): ToastApi {
  const api = useContext(Ctx);
  if (!api) throw new Error('useToast outside ToastProvider');
  return api;
}
