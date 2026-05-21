import { useEffect, useState } from 'react';
import { useLocation } from 'react-router-dom';
import { MdAdminPanelSettings, MdStorefront } from 'react-icons/md';
import { admin } from '../api/client.js';
import Card from '../components/Card.jsx';
import Spinner from '../components/Spinner.jsx';
import TopBar from '../components/TopBar.jsx';
import ThemeToggle from '../components/ThemeToggle.jsx';
import { brand } from '../brand/index.js';

// Top-level login page. On mount it probes /api/v1/auth/config. If a default
// OIDC provider is configured, it immediately redirects the browser there
// (full-page assign, not React navigation). ?manual=1 bypasses the redirect
// so break-glass admins can reach /admin/login directly.
export default function Login() {
  const loc = useLocation();
  const params = new URLSearchParams(loc.search);
  const next = params.get('next') || '/';
  const manual = params.get('manual') === '1';

  const [state, setState] = useState('loading'); // 'loading' | 'redirecting' | 'fallback'

  useEffect(() => {
    if (manual) {
      setState('fallback');
      return;
    }
    let cancelled = false;
    admin.authConfig()
      .then((cfg) => {
        if (cancelled) return;
        const provider = cfg?.default_provider;
        if (provider) {
          setState('redirecting');
          // Pass `next` directly as return_to. If next === '/' (the default),
          // omit return_to entirely so the server picks the role-based default
          // (/admin for admins, /catalog for everyone else).
          let url =
            '/api/v1/auth/oidc/' +
            encodeURIComponent(provider) +
            '/start?flow=auto';
          if (next && next !== '/') {
            url += '&return_to=' + encodeURIComponent(next);
          }
          window.location.assign(url);
        } else {
          setState('fallback');
        }
      })
      .catch(() => {
        if (!cancelled) setState('fallback');
      });
    return () => { cancelled = true; };
  }, [manual, next]);

  return (
    <div className="min-h-screen flex flex-col bg-g-canvas">
      <TopBar area="admin" linkBrand={false} rightSlot={<ThemeToggle />} />

      <div className="flex-1 flex items-center justify-center p-6">
        {state === 'loading' || state === 'redirecting' ? (
          <RedirectingPanel />
        ) : (
          <FallbackPanel />
        )}
      </div>

      <footer className="text-center py-4 text-xs text-g-text-disabled">
        {brand.footerTagline}
      </footer>
    </div>
  );
}

function RedirectingPanel() {
  const loc = useLocation();
  const params = new URLSearchParams(loc.search);
  const next = params.get('next') || '/';
  // Build the ?manual=1 URL preserving the next param.
  const manualUrl = '/login?manual=1' + (next !== '/' ? '&next=' + encodeURIComponent(next) : '');

  return (
    <div className="text-center space-y-4">
      <Spinner size="lg" />
      <p className="text-sm text-g-text-secondary">Redirecting to sign-in…</p>
      <a
        href={manualUrl}
        className="block text-xs text-g-text-disabled hover:text-g-text-secondary transition-colors"
      >
        Use credentials instead
      </a>
    </div>
  );
}

function FallbackPanel() {
  return (
    <div className="w-full max-w-md space-y-6">
      <div className="text-center">
        <h1 className="text-2xl font-semibold tracking-tight text-g-text">Sign in</h1>
        <p className="mt-2 text-sm text-g-text-secondary">{brand.embeddedTagline}</p>
      </div>

      <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <a href="/admin/login" className="block group">
          <Card elevated padding="lg" className="h-full hover:border-g-accent-main/50 transition-colors cursor-pointer">
            <div className="flex flex-col items-center text-center gap-3">
              <MdAdminPanelSettings className="text-3xl text-g-accent-text" />
              <div>
                <div className="font-semibold text-g-text">Admin sign-in</div>
                <div className="text-xs text-g-text-secondary mt-1">
                  Access the admin console with your administrator credentials.
                </div>
              </div>
            </div>
          </Card>
        </a>

        <a href="/catalog/login" className="block group">
          <Card elevated padding="lg" className="h-full hover:border-g-accent-main/50 transition-colors cursor-pointer">
            <div className="flex flex-col items-center text-center gap-3">
              <MdStorefront className="text-3xl text-g-accent-text" />
              <div>
                <div className="font-semibold text-g-text">Customer sign-in</div>
                <div className="text-xs text-g-text-secondary mt-1">
                  Access the package catalog with your customer credential.
                </div>
              </div>
            </div>
          </Card>
        </a>
      </div>
    </div>
  );
}
