import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import * as api from '../api';
import { ApiError } from '../api/client';
import type { User } from '../api/types';
import { allClaims, forgetClaim } from '../lib/claim-tokens';
import { isIsolated, signInHandoffUrl } from '../lib/isolation';

export interface AuthState {
  user: User | null;
  /** True until the first /auth/me answer. */
  loading: boolean;
  refresh(): Promise<User | null>;
  signOut(): Promise<void>;
  /**
   * Open the sign-in dialog; `next` runs after a successful sign-in.
   * On a cross-origin isolated document (the job page) there is no dialog:
   * the call hands off to `/jobs?signin=1&next=<this page>` with a full
   * load and `next` is dropped — the page itself is reloaded after sign-in,
   * so callers on the job page must not rely on in-memory side effects.
   */
  openSignIn(next?: () => void): void;
  closeSignIn(): void;
  signInOpen: boolean;
  /** Called by the dialog once a session exists. */
  onSignedIn(user: User): Promise<void>;
}

const Ctx = createContext<AuthState | null>(null);

export const ME_KEY = ['auth', 'me'] as const;

export function AuthProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const [signInOpen, setSignInOpen] = useState(false);
  const [after, setAfter] = useState<(() => void) | null>(null);

  const query = useQuery({
    queryKey: ME_KEY,
    queryFn: async () => {
      try {
        return await api.me();
      } catch (err) {
        if (err instanceof ApiError && err.status === 401) return null;
        throw err;
      }
    },
    staleTime: 60_000,
    retry: false,
  });

  const refresh = useCallback(async () => {
    const r = await query.refetch();
    return r.data ?? null;
  }, [query]);

  const signOut = useCallback(async () => {
    try {
      await api.logout();
    } finally {
      qc.setQueryData(ME_KEY, null);
      qc.removeQueries({ queryKey: ['jobs'] });
      qc.removeQueries({ queryKey: ['admin'] });
    }
  }, [qc]);

  const openSignIn = useCallback((next?: () => void) => {
    if (isIsolated()) {
      // The job page is cross-origin isolated (COOP same-origin for
      // SharedArrayBuffer), which also blocks the Google sign-in popup. Hand
      // off to the list with a full page load; it opens the dialog and comes
      // back here after sign-in.
      window.location.assign(signInHandoffUrl(window.location.pathname + window.location.search));
      return;
    }
    setAfter(() => next ?? null);
    setSignInOpen(true);
  }, []);
  const closeSignIn = useCallback(() => {
    setSignInOpen(false);
    setAfter(null);
  }, []);

  const onSignedIn = useCallback(
    async (user: User) => {
      qc.setQueryData(ME_KEY, user);
      // Attach the visitor's anonymous jobs to the account (README: "attach to account").
      const claims = allClaims();
      await Promise.all(
        Object.entries(claims).map(async ([jobId, token]) => {
          try {
            await api.claimJob(jobId, token);
            forgetClaim(jobId);
          } catch (err) {
            // 404 (deleted/expired) or 409 (already claimed): nothing to keep.
            if (err instanceof ApiError && (err.status === 404 || err.status === 409)) forgetClaim(jobId);
          }
        }),
      );
      await qc.invalidateQueries({ queryKey: ['jobs'] });
      setSignInOpen(false);
      const cb = after;
      setAfter(null);
      cb?.();
    },
    [after, qc],
  );

  useEffect(() => {
    // A session that dies mid-visit (deactivation, expiry) is noticed on focus.
    const onFocus = () => {
      if (query.data) void query.refetch();
    };
    window.addEventListener('focus', onFocus);
    return () => window.removeEventListener('focus', onFocus);
  }, [query]);

  const value = useMemo<AuthState>(
    () => ({
      user: query.data ?? null,
      loading: query.isPending,
      refresh,
      signOut,
      openSignIn,
      closeSignIn,
      signInOpen,
      onSignedIn,
    }),
    [query.data, query.isPending, refresh, signOut, openSignIn, closeSignIn, signInOpen, onSignedIn],
  );

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useAuth(): AuthState {
  const v = useContext(Ctx);
  if (!v) throw new Error('useAuth outside AuthProvider');
  return v;
}
