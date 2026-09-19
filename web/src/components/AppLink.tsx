import type { AnchorHTMLAttributes, ReactNode } from 'react';
import { Link } from '@tanstack/react-router';
import { crossesIsolation } from '../lib/isolation';
import { hrefOf, type AppTarget } from '../lib/use-app-navigate';

type Props = AppTarget & Omit<AnchorHTMLAttributes<HTMLAnchorElement>, 'href'> & { children?: ReactNode };

/**
 * Router link that falls back to a plain anchor (full page load) when the
 * target sits on the other side of the cross-origin isolation boundary:
 * list → job page needs the isolation headers, job page → anywhere else must
 * drop them so the Google popup works there.
 */
export function AppLink(props: Props) {
  if (props.to === '/jobs/$id') {
    const { to: _to, params, ...rest } = props;
    const href = hrefOf({ to: '/jobs/$id', params });
    if (crossesIsolation(href)) return <a href={href} {...rest} />;
    return <Link to="/jobs/$id" params={params} {...rest} />;
  }
  const { to, ...rest } = props;
  if (crossesIsolation(to)) return <a href={to} {...rest} />;
  return <Link to={to} {...rest} />;
}
