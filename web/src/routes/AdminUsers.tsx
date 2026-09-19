import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import * as api from '../api';
import { errorMessage } from '../api/client';
import type { Page, User } from '../api/types';
import { useAuth } from '../auth/AuthProvider';
import { Badge } from '../components/Badge';
import { ConfirmDialog } from '../components/Dialog';
import { EmptyState } from '../components/EmptyState';
import { Avatar } from '../components/Header';
import { Icon } from '../components/Icon';
import { useToast } from '../components/Toast';
import { formatDate } from '../lib/format';
import { useDebounced } from '../lib/use-debounced';
import { RequireUser } from './guards';

export type AdminFilter = 'all' | 'active' | 'pending' | 'admins';

export interface AdminSearch {
  q?: string;
  filter?: AdminFilter;
}

export function adminSearchSchema(raw: Record<string, unknown>): AdminSearch {
  const out: AdminSearch = {};
  if (typeof raw.q === 'string' && raw.q) out.q = raw.q;
  if (raw.filter === 'active' || raw.filter === 'pending' || raw.filter === 'admins') out.filter = raw.filter;
  return out;
}

const FILTERS: { id: AdminFilter; label: string }[] = [
  { id: 'all', label: 'ALL' },
  { id: 'active', label: 'ACTIVE' },
  { id: 'pending', label: 'PENDING' },
  { id: 'admins', label: 'ADMINS' },
];

const USERS_KEY = ['admin', 'users'] as const;

/** screens/08-admin-users, admins only. */
export function AdminUsersRoute() {
  return (
    <RequireUser admin title="User Management">
      <AdminUsersPage />
    </RequireUser>
  );
}

type Pending = { kind: 'role'; user: User; role: 'user' | 'admin' } | { kind: 'deactivate'; user: User } | null;

function AdminUsersPage() {
  const { user: me } = useAuth();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { toast } = useToast();
  const [q, setQ] = useState('');
  const [filter, setFilter] = useState<AdminFilter>('all');
  const debouncedQ = useDebounced(q, 250);
  const [confirm, setConfirm] = useState<Pending>(null);
  const [inFlight, setInFlight] = useState<Set<string>>(new Set());

  useEffect(() => {
    document.title = 'User Management — RehearseKit';
  }, []);

  // One request for everything: the table filters client-side so the stat
  // tiles cannot disagree with the rows (design note).
  const users = useQuery({
    queryKey: [...USERS_KEY, debouncedQ],
    queryFn: () => api.listUsers({ status: 'all', q: debouncedQ || undefined, page: 1, page_size: 100 }),
  });

  const all = useMemo(() => users.data?.items ?? [], [users.data]);
  const stats = useMemo(
    () => ({
      total: users.data?.total ?? all.length,
      active: all.filter((u) => u.status === 'active').length,
      pending: all.filter((u) => u.status === 'pending').length,
      admins: all.filter((u) => u.role === 'admin' && u.status === 'active').length,
    }),
    [all, users.data?.total],
  );
  const rows = useMemo(() => {
    switch (filter) {
      case 'active':
        return all.filter((u) => u.status === 'active');
      case 'pending':
        return all.filter((u) => u.status === 'pending');
      case 'admins':
        return all.filter((u) => u.role === 'admin');
      default:
        return all;
    }
  }, [all, filter]);

  const patch = (u: User) =>
    qc.setQueriesData<Page<User>>({ queryKey: USERS_KEY }, (cur) =>
      cur ? { ...cur, items: cur.items.map((x) => (x.id === u.id ? { ...x, ...u } : x)) } : cur,
    );

  const mutate = useMutation({
    mutationFn: async (a: { user: User; run: () => Promise<User>; optimistic: Partial<User>; failTitle: string }) => {
      patch({ ...a.user, ...a.optimistic });
      setInFlight((s) => new Set(s).add(a.user.id));
      try {
        return await a.run();
      } catch (err) {
        patch(a.user);
        toast({ kind: 'error', title: a.failTitle, detail: `${errorMessage(err, 'The server rejected the change.')} Nothing was applied — try again.` });
        throw err;
      } finally {
        setInFlight((s) => {
          const n = new Set(s);
          n.delete(a.user.id);
          return n;
        });
      }
    },
    onSuccess: (u) => patch(u),
  });

  const approve = (u: User) =>
    mutate.mutate({ user: u, run: () => api.approveUser(u.id), optimistic: { status: 'active' }, failTitle: `Could not approve ${u.name || u.email}` });
  const deactivate = (u: User) =>
    mutate.mutate({ user: u, run: () => api.deactivateUser(u.id), optimistic: { status: 'inactive' }, failTitle: `Could not deactivate ${u.name || u.email}` });
  const setRole = (u: User, role: 'user' | 'admin') =>
    mutate.mutate({ user: u, run: () => api.setUserRole(u.id, role), optimistic: { role }, failTitle: `Could not change ${u.name || u.email}'s role` });

  const clear = () => {
    setQ('');
    setFilter('all');
  };

  return (
    <main className="rk-shell" style={{ paddingTop: 'var(--rk-space-11)', position: 'relative' }}>
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', gap: 'var(--rk-space-8)', marginBottom: 'var(--rk-space-9)', flexWrap: 'wrap' }}>
        <div>
          <h1 className="rk-title">User Management</h1>
          <p className="rk-help" style={{ margin: 'var(--rk-space-3) 0 0' }}>
            Approve new accounts, change roles, and deactivate access.
          </p>
        </div>
        <Badge tone="outline">ADMINS ONLY</Badge>
      </div>

      <div className="rk-stats" style={{ marginBottom: 'var(--rk-space-9)' }}>
        <Stat value={stats.total} label="TOTAL USERS" />
        <Stat value={stats.active} label="ACTIVE USERS" />
        <Stat value={stats.pending} label="PENDING APPROVAL" onClick={stats.pending > 0 ? () => setFilter('pending') : undefined} />
        <Stat value={stats.admins} label="ADMINS" />
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--rk-space-6)', marginBottom: 'var(--rk-space-8)', flexWrap: 'wrap' }}>
        <div className="rk-field" style={{ flex: 1, minWidth: 240, maxWidth: 340 }}>
          <input className="rk-input" type="search" placeholder="Search users by name or email" aria-label="Search users" value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        <div className="rk-seg" role="group" aria-label="Filter by status">
          {FILTERS.map((f) => (
            <button key={f.id} type="button" aria-pressed={filter === f.id} onClick={() => setFilter(f.id)}>
              {f.label}
            </button>
          ))}
        </div>
      </div>

      {users.isPending ? (
        <div className="rk-tablewrap" aria-busy="true">
          <table className="rk-table">
            <thead>
              <tr>
                <th scope="col">USER</th>
                <th scope="col">PROVIDER</th>
                <th scope="col">ROLE</th>
                <th scope="col">STATUS</th>
                <th scope="col">JOINED</th>
                <th scope="col" />
              </tr>
            </thead>
            <tbody>
              {[0, 1, 2].map((i) => (
                <tr key={i}>
                  <td colSpan={6}>
                    <div className="rk-skel" style={{ height: 28 }} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : users.isError ? (
        <EmptyState art="error" title="Could not load users" action={<button className="rk-btn" type="button" onClick={() => void users.refetch()}>Try again</button>}>
          {errorMessage(users.error)}
        </EmptyState>
      ) : rows.length === 0 ? (
        <EmptyState title={debouncedQ ? `No users match “${debouncedQ}”` : 'No users in this filter'} action={<button className="rk-btn" type="button" onClick={clear}>{debouncedQ ? 'Clear search' : 'Clear filter'}</button>}>
          {debouncedQ ? `Search covers names and email addresses. Clear the search to see all ${stats.total} users.` : `Nothing matches this status. Clear the filter to see all ${stats.total} users.`}
        </EmptyState>
      ) : (
        <div className="rk-tablewrap">
          <table className="rk-table">
            <caption className="visually-hidden">Users with provider, role, status, join date and actions</caption>
            <thead>
              <tr>
                <th scope="col">USER</th>
                <th scope="col">PROVIDER</th>
                <th scope="col">ROLE</th>
                <th scope="col">STATUS</th>
                <th scope="col">JOINED</th>
                <th scope="col" />
              </tr>
            </thead>
            <tbody>
              {rows.map((u) => {
                const self = u.id === me?.id;
                const busy = inFlight.has(u.id);
                return (
                  <tr key={u.id} data-pending={u.status === 'pending' ? 'true' : undefined}>
                    <td>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--rk-space-5)' }}>
                        <Avatar user={u} size="md" copper={self} />
                        <div>
                          <div>{u.name || '—'}</div>
                          <div className="rk-help rk-mono">{u.email}</div>
                        </div>
                      </div>
                    </td>
                    <td>
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--rk-space-3)' }}>
                        <Icon name={u.provider === 'google' ? 'google' : 'mail'} size={14} />
                        {u.provider === 'google' ? 'Google' : 'Email'}
                      </span>
                    </td>
                    <td>{u.role === 'admin' ? <Badge tone="outline">ADMIN</Badge> : 'Member'}</td>
                    <td>
                      {u.status === 'active' ? <Badge tone="done">ACTIVE</Badge> : u.status === 'pending' ? <Badge tone="outline">PENDING</Badge> : <Badge tone="failed">INACTIVE</Badge>}
                    </td>
                    <td className="rk-mono">{formatDate(u.created_at)}</td>
                    <td>
                      <div style={{ display: 'flex', gap: 'var(--rk-space-4)', justifyContent: 'flex-end' }}>
                        {u.status === 'pending' ? (
                          <button className="rk-btn rk-btn--sm" type="button" disabled={busy} aria-busy={busy || undefined} onClick={() => approve(u)}>
                            <Icon name="check" /> Approve
                          </button>
                        ) : u.status === 'inactive' ? (
                          <button className="rk-btn rk-btn--sm" type="button" disabled={busy} aria-busy={busy || undefined} onClick={() => approve(u)}>
                            <Icon name="refresh" /> Reactivate
                          </button>
                        ) : u.role === 'admin' ? (
                          <button className="rk-btn rk-btn--sm" type="button" disabled={busy || self} title={self ? 'You cannot change your own role here' : undefined} onClick={() => setConfirm({ kind: 'role', user: u, role: 'user' })}>
                            <Icon name="shield" /> Remove admin
                          </button>
                        ) : (
                          <button className="rk-btn rk-btn--sm" type="button" disabled={busy} onClick={() => setConfirm({ kind: 'role', user: u, role: 'admin' })}>
                            <Icon name="shield" /> Make admin
                          </button>
                        )}
                        <RowMenu
                          user={u}
                          self={self}
                          busy={busy}
                          onMakeAdmin={() => setConfirm({ kind: 'role', user: u, role: 'admin' })}
                          onRemoveAdmin={() => setConfirm({ kind: 'role', user: u, role: 'user' })}
                          onDeactivate={() => setConfirm({ kind: 'deactivate', user: u })}
                          onApprove={() => approve(u)}
                        />
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      <ConfirmDialog
        open={confirm?.kind === 'deactivate'}
        title={`Deactivate ${confirm?.user.name || confirm?.user.email || ''}?`}
        body="They lose access immediately and their jobs stop processing. Completed jobs and stored stems are kept, and you can reactivate the account later."
        cancelLabel="Keep active"
        confirmLabel="Deactivate user"
        danger
        onCancel={() => setConfirm(null)}
        onConfirm={() => {
          if (confirm?.kind === 'deactivate') deactivate(confirm.user);
          setConfirm(null);
        }}
      />
      <ConfirmDialog
        open={confirm?.kind === 'role'}
        title={confirm?.kind === 'role' && confirm.role === 'admin' ? `Make ${confirm.user.name || confirm.user.email} an admin?` : `Remove admin from ${confirm?.user.name || confirm?.user.email || ''}?`}
        body={
          confirm?.kind === 'role' && confirm.role === 'admin'
            ? 'Admins can approve accounts, change roles and deactivate anyone — including other admins.'
            : 'They keep their account and jobs, but can no longer manage users.'
        }
        cancelLabel="Keep as is"
        confirmLabel={confirm?.kind === 'role' && confirm.role === 'admin' ? 'Make admin' : 'Remove admin'}
        onCancel={() => setConfirm(null)}
        onConfirm={() => {
          if (confirm?.kind === 'role') setRole(confirm.user, confirm.role);
          setConfirm(null);
        }}
      />
      <div style={{ marginTop: 'var(--rk-space-9)' }}>
        <button className="rk-btn rk-btn--sm" type="button" onClick={() => void navigate({ to: '/jobs' })}>
          <Icon name="arrow-left" /> Back to jobs
        </button>
      </div>
    </main>
  );
}

function Stat({ value, label, onClick }: { value: number; label: string; onClick?: () => void }) {
  const inner = (
    <>
      <b>{value}</b>
      <span>{label}</span>
    </>
  );
  if (onClick)
    return (
      <button className="rk-stat" type="button" onClick={onClick} style={{ cursor: 'pointer', textAlign: 'left' }}>
        {inner}
      </button>
    );
  return <div className="rk-stat">{inner}</div>;
}

function RowMenu(p: { user: User; self: boolean; busy: boolean; onMakeAdmin(): void; onRemoveAdmin(): void; onDeactivate(): void; onApprove(): void }) {
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (!open) return;
    const close = () => setOpen(false);
    document.addEventListener('mousedown', close);
    return () => document.removeEventListener('mousedown', close);
  }, [open]);
  const u = p.user;
  return (
    <div style={{ position: 'relative' }} onMouseDown={(e) => e.stopPropagation()}>
      <button className="rk-iconbtn" type="button" aria-label={`More actions for ${u.name || u.email}`} aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((o) => !o)} disabled={p.busy}>
        <Icon name="more" />
      </button>
      {open && (
        <div className="rk-menu rk-menu--anchored" role="menu">
          {u.status === 'pending' && (
            <button className="rk-menu-item" role="menuitem" type="button" onClick={() => { setOpen(false); p.onApprove(); }}>
              <Icon name="check" /> Approve
            </button>
          )}
          {u.role === 'admin' ? (
            <button className="rk-menu-item" role="menuitem" type="button" disabled={p.self} onClick={() => { setOpen(false); p.onRemoveAdmin(); }}>
              <Icon name="shield" /> Remove admin
            </button>
          ) : (
            <button className="rk-menu-item" role="menuitem" type="button" onClick={() => { setOpen(false); p.onMakeAdmin(); }}>
              <Icon name="shield" /> Make admin
            </button>
          )}
          <hr />
          <button className="rk-menu-item" role="menuitem" type="button" data-danger="true" disabled={p.self || u.status === 'inactive'} onClick={() => { setOpen(false); p.onDeactivate(); }}>
            <Icon name="x" /> Deactivate
          </button>
        </div>
      )}
    </div>
  );
}
