import type { CSSProperties, ReactNode } from 'react';

/** Console panel with the four corner screws (screens use it for controls). */
export function Panel({ children, style, className, screws = true }: { children: ReactNode; style?: CSSProperties; className?: string; screws?: boolean }) {
  return (
    <div className={className ? `rk-panel ${className}` : 'rk-panel'} style={style}>
      {screws && (
        <>
          <div className="rk-screw" style={{ left: 8, top: 8 }} />
          <div className="rk-screw" style={{ right: 8, top: 8 }} />
          <div className="rk-screw" style={{ left: 8, bottom: 8 }} />
          <div className="rk-screw" style={{ right: 8, bottom: 8 }} />
        </>
      )}
      {children}
    </div>
  );
}

export function Divider({ children }: { children: ReactNode }) {
  return (
    <div className="rk-divider">
      <i />
      <span>{children}</span>
      <i />
    </div>
  );
}
