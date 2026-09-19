import { useCallback } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { crossesIsolation } from './isolation';

export type AppTarget =
  | { to: '/' | '/jobs' | '/profile' | '/admin/users' | '/pending-approval' }
  | { to: '/jobs/$id'; params: { id: string } };

export function hrefOf(t: AppTarget): string {
  return t.to === '/jobs/$id' ? `/jobs/${encodeURIComponent(t.params.id)}` : t.to;
}

/**
 * Router navigation that turns into a full page load when the target sits on
 * the other side of the cross-origin isolation boundary (see lib/isolation).
 */
export function useAppNavigate(): (t: AppTarget) => void {
  const navigate = useNavigate();
  return useCallback(
    (t: AppTarget) => {
      const href = hrefOf(t);
      if (crossesIsolation(href)) {
        window.location.assign(href);
        return;
      }
      if (t.to === '/jobs/$id') void navigate({ to: '/jobs/$id', params: t.params });
      else void navigate({ to: t.to });
    },
    [navigate],
  );
}
