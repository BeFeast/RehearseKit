/**
 * Google Identity Services (the `gsi/client` script) — button flow only.
 * One Tap is never prompted: the dialog renders the official button, the
 * visitor clicks it, GIS opens its popup and hands back an ID token that
 * POST /api/v1/auth/google verifies. The popup needs a document without
 * COOP same-origin, which is why the job page hands off to /jobs first
 * (see lib/isolation).
 */

/** `hl` pins the button copy to English (the rest of the UI is English); renderButton's `locale` alone does not override the browser locale. */
export const GIS_SRC = 'https://accounts.google.com/gsi/client?hl=en';

export interface CredentialResponse {
  credential: string;
  select_by?: string;
}

interface ButtonConfig {
  type?: 'standard' | 'icon';
  theme?: 'outline' | 'filled_blue' | 'filled_black';
  size?: 'large' | 'medium' | 'small';
  text?: 'signin_with' | 'signup_with' | 'continue_with' | 'signin';
  shape?: 'rectangular' | 'pill' | 'circle' | 'square';
  logo_alignment?: 'left' | 'center';
  width?: number;
  locale?: string;
}

interface GoogleId {
  initialize(config: {
    client_id: string;
    callback: (r: CredentialResponse) => void;
    auto_select?: boolean;
    cancel_on_tap_outside?: boolean;
    ux_mode?: 'popup' | 'redirect';
    itp_support?: boolean;
    use_fedcm_for_prompt?: boolean;
  }): void;
  renderButton(parent: HTMLElement, options: ButtonConfig): void;
  disableAutoSelect(): void;
}

export interface GoogleAccounts {
  accounts: { id: GoogleId };
}

declare global {
  interface Window {
    google?: GoogleAccounts;
  }
}

let loading: Promise<GoogleAccounts> | null = null;

/** Load the GIS script once; resolves with `window.google`. */
export function loadGis(doc: Document = document): Promise<GoogleAccounts> {
  if (window.google?.accounts?.id) return Promise.resolve(window.google);
  if (loading) return loading;
  loading = new Promise<GoogleAccounts>((resolve, reject) => {
    const existing = doc.querySelector<HTMLScriptElement>(`script[src="${GIS_SRC}"]`);
    const script = existing ?? doc.createElement('script');
    const done = () => {
      if (window.google?.accounts?.id) resolve(window.google);
      else reject(new Error('Google Identity Services did not initialise'));
    };
    const fail = () => {
      loading = null;
      script.remove();
      reject(new Error('Could not load Google sign-in'));
    };
    script.addEventListener('load', done, { once: true });
    script.addEventListener('error', fail, { once: true });
    if (!existing) {
      script.src = GIS_SRC;
      script.async = true;
      script.defer = true;
      doc.head.appendChild(script);
    }
  });
  return loading;
}

/** Test hook: forget an in-flight load. */
export function resetGisForTests(): void {
  loading = null;
}

/** Width GIS accepts for its button: 200..400 px. */
export function buttonWidth(containerWidth: number): number {
  return Math.max(200, Math.min(400, Math.floor(containerWidth)));
}
