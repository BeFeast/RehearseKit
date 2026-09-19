import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useQuery } from '@tanstack/react-query';
import * as api from '../api';
import { ApiError } from '../api/client';
import type { User } from '../api/types';
import { Dialog } from '../components/Dialog';
import { Icon } from '../components/Icon';
import { buttonWidth, loadGis, type CredentialResponse } from '../lib/gis';
import { useTheme } from '../lib/use-theme';
import { useAuth } from './AuthProvider';

type Mode = 'signin' | 'register';
type Banner =
  | { kind: 'wrong' }
  | { kind: 'not-approved' }
  | { kind: 'inactive' }
  | { kind: 'google-blocked' }
  | { kind: 'google-soon' }
  | { kind: 'google-unavailable' }
  | { kind: 'error'; message: string };

/** GIS button lifecycle inside the dialog. */
type GisState = 'off' | 'loading' | 'ready' | 'failed';

export const PENDING_EMAIL_KEY = 'rk.pendingEmail';

/**
 * Sign-in dialog (screens/05-sign-in): Google first, OR, email + password,
 * links to create an account. Registration swaps the same shell to a form
 * with name and password confirmation and ends on /pending-approval.
 *
 * Google is the official GIS button (popup flow, no One Tap): the ID token
 * from its callback goes to POST /auth/google as-is.
 */
export function SignInDialog() {
  const { signInOpen, closeSignIn, onSignedIn } = useAuth();
  const navigate = useNavigate();
  const [mode, setMode] = useState<Mode>('signin');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [banner, setBanner] = useState<Banner | null>(null);
  const [invalid, setInvalid] = useState(false);
  const [gis, setGis] = useState<GisState>('off');
  const gisHost = useRef<HTMLDivElement>(null);
  const theme = useTheme();

  const config = useQuery({ queryKey: ['config'], queryFn: api.getConfig, staleTime: Infinity, enabled: signInOpen });
  const clientId = config.data?.google_sign_in ? config.data.google_client_id : '';

  useEffect(() => {
    if (!signInOpen) {
      setMode('signin');
      setEmail('');
      setPassword('');
      setConfirm('');
      setName('');
      setBanner(null);
      setInvalid(false);
      setBusy(false);
      setGis('off');
    }
  }, [signInOpen]);

  const edit = (setter: (v: string) => void) => (v: string) => {
    setter(v);
    setBanner(null);
    setInvalid(false);
  };

  function pendingApproval(userEmail: string) {
    sessionStorage.setItem(PENDING_EMAIL_KEY, userEmail);
    closeSignIn();
    void navigate({ to: '/pending-approval' });
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setBanner(null);
    try {
      if (mode === 'signin') {
        const user = await api.login(email.trim(), password);
        await onSignedIn(user);
      } else {
        if (password !== confirm) {
          setBanner({ kind: 'error', message: 'The two passwords do not match.' });
          setInvalid(true);
          return;
        }
        await api.register(email.trim(), password, name.trim());
        pendingApproval(email.trim());
      }
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.code === 'invalid_credentials') {
          setBanner({ kind: 'wrong' });
          setInvalid(true);
        } else if (err.code === 'pending_approval') {
          sessionStorage.setItem(PENDING_EMAIL_KEY, email.trim());
          setBanner({ kind: 'not-approved' });
        } else if (err.code === 'account_inactive') {
          setBanner({ kind: 'inactive' });
        } else {
          setBanner({ kind: 'error', message: err.message });
          setInvalid(err.status === 400 || err.status === 409);
        }
      } else {
        setBanner({ kind: 'error', message: 'Could not reach the server. Check your connection and try again.' });
      }
    } finally {
      setBusy(false);
    }
  }

  // GIS calls the callback it was initialised with; keep it pointing at the
  // latest closure so state setters and onSignedIn are current.
  const onCredentialRef = useRef<(r: CredentialResponse) => void>(() => undefined);
  onCredentialRef.current = (r) => void onCredential(r);

  async function onCredential(r: CredentialResponse) {
    if (busy) return;
    setBusy(true);
    setBanner(null);
    setInvalid(false);
    try {
      const user = await api.googleSignIn(r.credential);
      await onSignedIn(user);
    } catch (err) {
      if (err instanceof ApiError) {
        const bodyUser = (err.body as { user?: Partial<User> } | null)?.user;
        if (err.code === 'pending_approval') {
          pendingApproval(bodyUser?.email ?? '');
        } else if (err.code === 'account_inactive') {
          setBanner({ kind: 'inactive' });
        } else if (err.code === 'email_not_verified') {
          setBanner({ kind: 'error', message: 'Google reports this email address as unverified. Verify it with Google, or sign in with an email and password.' });
        } else if (err.status === 401) {
          setBanner({ kind: 'error', message: 'Google did not confirm your identity. Try again, or sign in with an email and password.' });
        } else if (err.status === 501) {
          setBanner({ kind: 'google-soon' });
        } else if (err.status === 503) {
          setBanner({ kind: 'error', message: 'Google sign-in is temporarily unavailable — the server could not reach Google. Try again in a minute, or use an email and password.' });
        } else {
          setBanner({ kind: 'error', message: err.message });
        }
      } else {
        setBanner({ kind: 'error', message: 'Could not reach the server. Check your connection and try again.' });
      }
    } finally {
      setBusy(false);
    }
  }

  // Render the official button once the dialog is open in sign-in mode and
  // the server has a client id. renderButton needs the host in the DOM.
  useEffect(() => {
    if (!signInOpen || mode !== 'signin' || !clientId) return;
    let cancelled = false;
    setGis('loading');
    loadGis()
      .then((g) => {
        const host = gisHost.current;
        if (cancelled || !host) return;
        g.accounts.id.initialize({
          client_id: clientId,
          callback: (r) => onCredentialRef.current(r),
          auto_select: false,
          cancel_on_tap_outside: true,
          ux_mode: 'popup',
          itp_support: true,
        });
        host.replaceChildren();
        g.accounts.id.renderButton(host, {
          type: 'standard',
          theme: theme === 'dark' ? 'filled_black' : 'outline',
          size: 'large',
          text: 'continue_with',
          shape: 'rectangular',
          logo_alignment: 'center',
          // The rest of the dialog is English; GIS otherwise follows the browser locale.
          locale: 'en',
          width: buttonWidth(host.clientWidth || 372),
        });
        setGis('ready');
      })
      .catch(() => {
        if (!cancelled) setGis('failed');
      });
    return () => {
      cancelled = true;
    };
  }, [signInOpen, mode, clientId, theme]);

  function googleFallback() {
    // No GIS button to click: say why instead of failing silently.
    if (!clientId) setBanner({ kind: 'google-soon' });
    else if (gis === 'failed') setBanner({ kind: 'google-unavailable' });
  }

  const title = mode === 'signin' ? 'Sign in to RehearseKit' : 'Create your account';
  const lede =
    mode === 'signin'
      ? 'Keep your jobs in one place and come back to them later.'
      : 'An administrator approves new accounts, usually within 24-48 hours.';

  const googleButtonVisible = clientId && gis !== 'failed';

  return (
    <Dialog
      open={signInOpen}
      onClose={closeSignIn}
      pending={busy}
      title={
        <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 'var(--rk-space-6)' }}>
          <div>
            <h2 style={{ margin: 0 }}>{title}</h2>
            <p style={{ marginTop: 'var(--rk-space-3)' }}>{lede}</p>
          </div>
          <button className="rk-iconbtn" type="button" aria-label="Close" onClick={closeSignIn} disabled={busy}>
            <Icon name="x" />
          </button>
        </div>
      }
    >
      {banner && <SignInBanner banner={banner} />}
      <form onSubmit={submit} style={{ display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-7)' }}>
        {mode === 'signin' && (
          <>
            <div className="rk-google" data-state={googleButtonVisible ? gis : 'off'} data-testid="google-signin">
              {/* The GIS iframe button lands here; the styled button is the placeholder while it loads and the fallback when it cannot. */}
              <div ref={gisHost} className="rk-google-host" aria-busy={gis === 'loading' || undefined} />
              {gis !== 'ready' && (
                <button className="rk-btn rk-btn--lg rk-btn--block rk-google-fallback" type="button" onClick={googleFallback} disabled={busy || gis === 'loading'} aria-busy={gis === 'loading' || undefined}>
                  <Icon name="google" size={18} /> {gis === 'loading' ? 'Loading Google sign-in…' : 'Continue with Google'}
                </button>
              )}
            </div>
            <div className="rk-or">
              <i />
              OR
              <i />
            </div>
          </>
        )}
        {mode === 'register' && (
          <div className="rk-field">
            <label htmlFor="si-name">Full Name</label>
            <input className="rk-input" id="si-name" value={name} onChange={(e) => edit(setName)(e.target.value)} placeholder="Enter your full name" autoComplete="name" />
          </div>
        )}
        <div className="rk-field">
          <label htmlFor="si-email">Email</label>
          <input
            className="rk-input"
            id="si-email"
            type="email"
            value={email}
            onChange={(e) => edit(setEmail)(e.target.value)}
            placeholder="you@band.com"
            autoComplete="email"
            aria-invalid={invalid || undefined}
            required
          />
        </div>
        <div className="rk-field">
          <label htmlFor="si-password">Password</label>
          <input
            className="rk-input"
            id="si-password"
            type="password"
            value={password}
            onChange={(e) => edit(setPassword)(e.target.value)}
            autoComplete={mode === 'signin' ? 'current-password' : 'new-password'}
            aria-invalid={invalid || undefined}
            required
            minLength={mode === 'register' ? 8 : undefined}
          />
          {mode === 'register' && <span className="rk-help">At least 8 characters.</span>}
        </div>
        {mode === 'register' && (
          <div className="rk-field">
            <label htmlFor="si-confirm">Confirm password</label>
            <input className="rk-input" id="si-confirm" type="password" value={confirm} onChange={(e) => edit(setConfirm)(e.target.value)} autoComplete="new-password" required />
          </div>
        )}
        <button className="rk-btn rk-btn--primary rk-btn--lg rk-btn--block" type="submit" disabled={busy} aria-busy={busy || undefined}>
          {mode === 'signin' ? (busy ? 'Signing in…' : 'Sign in') : busy ? 'Creating account…' : 'Create account'}
        </button>
        <div style={{ display: 'flex', justifyContent: 'space-between', gap: 'var(--rk-space-6)' }}>
          {mode === 'signin' ? (
            <>
              <a href="#" style={{ fontSize: 'var(--rk-font-size-md)' }} onClick={(e) => { e.preventDefault(); setMode('register'); setBanner(null); setInvalid(false); }}>
                Create an account
              </a>
              <a
                href="#"
                style={{ fontSize: 'var(--rk-font-size-md)' }}
                onClick={(e) => {
                  e.preventDefault();
                  setBanner({ kind: 'error', message: 'Password reset is not available in this build — ask an administrator to reset it.' });
                }}
              >
                Forgot password
              </a>
            </>
          ) : (
            <a href="#" style={{ fontSize: 'var(--rk-font-size-md)' }} onClick={(e) => { e.preventDefault(); setMode('signin'); setBanner(null); setInvalid(false); }}>
              Already have an account? Sign in
            </a>
          )}
        </div>
      </form>
    </Dialog>
  );
}

function SignInBanner({ banner }: { banner: Banner }) {
  const navigate = useNavigate();
  const { closeSignIn } = useAuth();
  switch (banner.kind) {
    case 'wrong':
      return (
        <div className="rk-alert" role="alert">
          <Icon name="alert" size={18} />
          <div>
            <strong>That email and password do not match.</strong> Check the address, or reset your password. Accounts created with Google do not have a password — use Continue with Google.
          </div>
        </div>
      );
    case 'not-approved':
      return (
        <div className="rk-alert rk-alert--warn" role="status">
          <Icon name="alert" size={18} />
          <div>
            <strong>This account is waiting for approval.</strong> You will get an email when an administrator approves it.{' '}
            <a
              href="#"
              onClick={(e) => {
                e.preventDefault();
                closeSignIn();
                void navigate({ to: '/pending-approval' });
              }}
            >
              See your approval status
            </a>
            .
          </div>
        </div>
      );
    case 'inactive':
      return (
        <div className="rk-alert" role="alert">
          <Icon name="alert" size={18} />
          <div>
            <strong>This account has been deactivated.</strong> Contact an administrator to restore access.
          </div>
        </div>
      );
    case 'google-blocked':
      return (
        <div className="rk-alert rk-alert--warn" role="status">
          <Icon name="alert" size={18} />
          <div>
            <strong>The Google window was blocked.</strong> Allow pop-ups for this site and try again, or sign in with an email and password below.
          </div>
        </div>
      );
    case 'google-unavailable':
      return (
        <div className="rk-alert rk-alert--warn" role="status">
          <Icon name="alert" size={18} />
          <div>
            <strong>Google sign-in could not load.</strong> A content blocker or network filter may be stopping accounts.google.com — allow it and reload, or sign in with an email and password below.
          </div>
        </div>
      );
    case 'google-soon':
      return (
        <div className="rk-alert rk-alert--warn" role="status">
          <Icon name="alert" size={18} />
          <div>
            <strong>Google sign-in is not enabled on this server.</strong> Use an email and password below.
          </div>
        </div>
      );
    case 'error':
      return (
        <div className="rk-alert" role="alert">
          <Icon name="alert" size={18} />
          <div>{banner.message}</div>
        </div>
      );
  }
}
