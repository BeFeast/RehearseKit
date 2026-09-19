import { useEffect, useRef, useState } from 'react';
import { Link, useNavigate, useRouterState } from '@tanstack/react-router';
import { useQuery } from '@tanstack/react-query';
import * as api from '../api';
import { useAuth } from '../auth/AuthProvider';
import { initials } from '../lib/format';
import { toggleTheme } from '../lib/theme';
import { useTheme } from '../lib/use-theme';
import { Icon } from './Icon';
import { LogoMark } from './Logo';

/**
 * The persistent header (components/app-header/spec.md): brand, two nav
 * items, status pill, theme switch, then Sign In or the account menu. On
 * /pending-approval the nav links are dropped.
 */
export function Header() {
  const { user, openSignIn, signOut } = useAuth();
  const navigate = useNavigate();
  const path = useRouterState({ select: (s) => s.location.pathname });
  const theme = useTheme();
  const pendingRoute = path === '/pending-approval';
  const [menuOpen, setMenuOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  const healthQ = useQuery({ queryKey: ['health'], queryFn: api.health, refetchInterval: 60_000, staleTime: 30_000 });
  const healthState = healthQ.data ?? 'ok';

  useEffect(() => {
    if (!menuOpen) return;
    const onDown = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) setMenuOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setMenuOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [menuOpen]);

  const isCurrent = (p: string) => (p === '/' ? path === '/' : path === p || path.startsWith(`${p}/`));

  return (
    <header className="rk-header">
      <div className="rk-shell rk-header-inner">
        <Link className="rk-brand" to="/">
          <LogoMark />
          <span>RehearseKit</span>
        </Link>
        {!pendingRoute && (
          <nav className="rk-nav" aria-label="Primary">
            <Link to="/" aria-current={isCurrent('/') ? 'page' : undefined}>
              Home
            </Link>
            <Link to="/jobs" aria-current={isCurrent('/jobs') ? 'page' : undefined}>
              Jobs
            </Link>
          </nav>
        )}
        <div className="rk-spacer" />
        <div className="rk-status-pill" title="API readiness (/readyz)">
          <span className="rk-status-dot" data-state={healthState} />
          {healthState === 'ok' ? 'Operational' : healthState === 'degraded' ? 'Degraded' : 'Unreachable'}
        </div>
        <button
          className="rk-theme-switch"
          type="button"
          onClick={() => toggleTheme()}
          aria-pressed={theme === 'dark'}
          aria-label={theme === 'dark' ? 'Switch to the light theme' : 'Switch to the dark theme'}
          title="Theme"
        >
          {theme === 'dark' ? 'CONSOLE' : 'SAND'}
        </button>
        {user ? (
          <div style={{ position: 'relative' }} ref={menuRef}>
            <button
              className="rk-avatarbtn"
              type="button"
              aria-haspopup="menu"
              aria-expanded={menuOpen}
              onClick={() => setMenuOpen((o) => !o)}
              data-testid="account-menu-button"
            >
              <Avatar user={{ name: user.name, email: user.email, avatar_url: user.avatar_url }} size="lg" copper />
              <Icon name="chevron-down" size={16} />
            </button>
            {menuOpen && (
              <div className="rk-menu rk-menu--anchored" role="menu">
                <div className="rk-menu-label">SIGNED IN AS {user.email.toUpperCase()}</div>
                {!pendingRoute && (
                  <button
                    className="rk-menu-item"
                    role="menuitem"
                    type="button"
                    onClick={() => {
                      setMenuOpen(false);
                      void navigate({ to: '/profile' });
                    }}
                  >
                    <Icon name="user" /> Profile
                  </button>
                )}
                {!pendingRoute && user.role === 'admin' && (
                  <button
                    className="rk-menu-item"
                    role="menuitem"
                    type="button"
                    onClick={() => {
                      setMenuOpen(false);
                      void navigate({ to: '/admin/users' });
                    }}
                  >
                    <Icon name="shield" /> User Management
                  </button>
                )}
                <button
                  className="rk-menu-item"
                  role="menuitem"
                  type="button"
                  onClick={() => toggleTheme()}
                >
                  <Icon name="settings" /> Theme: {theme === 'dark' ? 'Console' : 'Sand'}
                </button>
                <hr />
                <button
                  className="rk-menu-item"
                  role="menuitem"
                  type="button"
                  onClick={async () => {
                    setMenuOpen(false);
                    await signOut();
                    void navigate({ to: '/' });
                  }}
                >
                  <Icon name="logout" /> Log out
                </button>
              </div>
            )}
          </div>
        ) : pendingRoute ? (
          <button className="rk-btn" type="button" onClick={() => void navigate({ to: '/' })}>
            <Icon name="logout" /> Log out
          </button>
        ) : (
          <button className="rk-btn" type="button" onClick={() => openSignIn()} data-testid="sign-in-button">
            Sign In
          </button>
        )}
      </div>
    </header>
  );
}

export interface AvatarProps {
  user: { name: string; email: string; avatar_url?: string | null };
  size?: 'sm' | 'md' | 'lg' | 'xl';
  copper?: boolean;
}

export function Avatar({ user, size = 'md', copper = false }: AvatarProps) {
  const cls = `rk-av rk-av--${size}${copper ? ' rk-av--copper' : ''}`;
  if (user.avatar_url) {
    return (
      <span className={cls}>
        <img src={user.avatar_url} alt="" style={{ width: '100%', height: '100%', objectFit: 'cover' }} />
      </span>
    );
  }
  return <span className={cls}>{initials(user.name, user.email)}</span>;
}
