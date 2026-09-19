import { useEffect, useState, type FormEvent } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import * as api from '../api';
import { ApiError, errorMessage } from '../api/client';
import { ME_KEY, useAuth } from '../auth/AuthProvider';
import { Avatar } from '../components/Header';
import { Badge } from '../components/Badge';
import { Icon } from '../components/Icon';
import { useToast } from '../components/Toast';
import { formatDateLong, formatDateTime } from '../lib/format';
import { RequireUser } from './guards';

/** screens/07-profile: view, editing, saving, saved (toast), error. */
export function ProfileRoute() {
  return (
    <RequireUser>
      <ProfilePage />
    </RequireUser>
  );
}

function ProfilePage() {
  const { user: sessionUser } = useAuth();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { toast } = useToast();
  const profile = useQuery({ queryKey: ['profile'], queryFn: api.getProfile, initialData: sessionUser ?? undefined });
  const user = profile.data ?? sessionUser!;

  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(user.name);
  const [avatar, setAvatar] = useState(user.avatar_url ?? '');
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    document.title = 'Profile — RehearseKit';
  }, []);

  const save = useMutation({
    mutationFn: (patch: api.ProfilePatch) => api.patchProfile(patch),
    onSuccess: (u) => {
      qc.setQueryData(['profile'], u);
      qc.setQueryData(ME_KEY, u);
      setEditing(false);
      setError(null);
      toast({ kind: 'success', title: 'Profile saved', detail: 'Your name and avatar are updated everywhere.' });
    },
    onError: (err) => {
      const msg =
        err instanceof ApiError && err.unavailable
          ? 'Profile editing is not available in this build yet. Nothing was changed.'
          : errorMessage(err, 'The server rejected the change.');
      setError(msg);
      toast({ kind: 'error', title: 'Could not save profile', detail: msg });
    },
  });

  function startEdit() {
    setName(user.name);
    setAvatar(user.avatar_url ?? '');
    setError(null);
    setEditing(true);
  }

  function cancel() {
    const dirty = name !== user.name || avatar !== (user.avatar_url ?? '');
    if (dirty && !window.confirm('Discard your changes?')) return;
    setEditing(false);
    setError(null);
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    const trimmed = avatar.trim();
    if (trimmed && !/^https?:\/\//i.test(trimmed)) {
      setError('The avatar URL must start with http:// or https://. Nothing was changed.');
      return;
    }
    save.mutate({ name: name.trim(), avatar_url: trimmed || null });
  }

  const providerLabel = user.provider === 'google' ? 'Google' : 'Email and password';

  return (
    <main className="rk-shell" style={{ paddingTop: 'var(--rk-space-11)', position: 'relative' }}>
      <div style={{ maxWidth: 760, display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-11)' }}>
        <div>
          <h1 className="rk-title">Profile</h1>
          <p className="rk-help" style={{ margin: 'var(--rk-space-3) 0 0' }}>
            How you appear in RehearseKit, and what the account is signed in with.
          </p>
        </div>

        <section className="rk-flat" style={{ padding: 'var(--rk-space-9)' }}>
          <h2 className="rk-h2" style={{ marginBottom: 'var(--rk-space-8)' }}>
            Profile Information
          </h2>
          {editing ? (
            <>
              {error && (
                <div className="rk-alert" style={{ marginBottom: 'var(--rk-space-7)' }} role="alert">
                  <Icon name="alert" size={18} />
                  <div>
                    <strong>Could not save your profile.</strong> {error}
                  </div>
                </div>
              )}
              <form onSubmit={submit} style={{ display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-7)' }} aria-busy={save.isPending || undefined}>
                <div className="rk-field">
                  <label htmlFor="pf-name">Full Name</label>
                  <input className="rk-input" id="pf-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Enter your full name" readOnly={save.isPending} />
                </div>
                <div className="rk-field">
                  <label htmlFor="pf-avatar">Avatar URL</label>
                  <input className="rk-input rk-input--mono" id="pf-avatar" value={avatar} onChange={(e) => setAvatar(e.target.value)} placeholder="https://" readOnly={save.isPending} />
                  <span className="rk-help">Leave empty to use your initials.</span>
                </div>
                <div style={{ display: 'flex', gap: 'var(--rk-space-5)' }}>
                  <button className="rk-btn rk-btn--primary" type="submit" disabled={save.isPending} aria-busy={save.isPending || undefined}>
                    {save.isPending ? 'Saving…' : 'Save changes'}
                  </button>
                  <button className="rk-btn" type="button" onClick={cancel} disabled={save.isPending}>
                    Cancel
                  </button>
                </div>
              </form>
            </>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--rk-space-7)' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--rk-space-8)' }}>
                <Avatar user={user} size="xl" copper />
                <div>
                  <div style={{ fontSize: 'var(--rk-font-size-2xl)', fontWeight: 600 }}>{user.name || user.email}</div>
                  <div className="rk-help" style={{ marginTop: 'var(--rk-space-3)' }}>
                    {user.avatar_url ? 'Avatar loaded from the URL on file.' : 'Avatar URL not set — showing initials.'}
                  </div>
                </div>
              </div>
              <div>
                <button className="rk-btn" type="button" onClick={startEdit}>
                  Edit profile
                </button>
              </div>
            </div>
          )}
        </section>

        <section className="rk-flat" style={{ padding: 'var(--rk-space-9)' }}>
          <h2 className="rk-h2" style={{ marginBottom: 'var(--rk-space-6)' }}>
            Account Details
          </h2>
          <Row label="Email">
            <span className="rk-mono">{user.email}</span>
          </Row>
          <Row label="Authentication Provider">
            <span>{providerLabel}</span>
          </Row>
          <Row label="Member Since">
            <span>{formatDateLong(user.created_at)}</span>
          </Row>
          <Row label="Last Login">
            <span className="rk-mono">{formatDateTime(user.last_login_at)}</span>
          </Row>
          <Row label="Role" last>
            <Badge tone="outline">{user.role.toUpperCase()}</Badge>
          </Row>
        </section>
        {user.role === 'admin' && (
          <div>
            <button className="rk-btn" type="button" onClick={() => void navigate({ to: '/admin/users' })}>
              <Icon name="shield" /> User Management
            </button>
          </div>
        )}
      </div>
    </main>
  );
}

function Row({ label, children, last = false }: { label: string; children: React.ReactNode; last?: boolean }) {
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'baseline',
        justifyContent: 'space-between',
        gap: 'var(--rk-space-6)',
        padding: 'var(--rk-space-6) 0',
        borderBottom: last ? undefined : '1px solid var(--rk-color-line)',
      }}
    >
      <span className="rk-help">{label}</span>
      {children}
    </div>
  );
}
