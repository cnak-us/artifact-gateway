import { useEffect, useMemo, useState } from 'react';
import { useNavigate, useLocation, Link } from 'react-router-dom';
import { MdLogin } from 'react-icons/md';
import { catalog as catalogApi } from '../../api/client.js';
import { useCatalogAuth } from '../../contexts/CatalogAuthContext.jsx';
import ErrorBanner from '../../components/ErrorBanner.jsx';
import Spinner from '../../components/Spinner.jsx';
import TopBar from '../../components/TopBar.jsx';
import ThemeToggle from '../../components/ThemeToggle.jsx';
import { brand } from '../../brand/index.js';

// Customer login. Accepts either:
//   - a combined "token_id:secret" paste, OR
//   - split tokenId / secret fields.
// The combined form is what `docker login -u <id> -p <secret>` users will
// have on hand; the split form matches what we display when we issue a
// brand-new credential.
export default function CatalogLogin() {
  const nav = useNavigate();
  const loc = useLocation();
  const { session, login } = useCatalogAuth();
  const next = new URLSearchParams(loc.search).get('next') || '/catalog';

  const [mode, setMode] = useState('combined'); // 'combined' | 'split'
  const [combined, setCombined] = useState('');
  const [tokenId, setTokenId] = useState('');
  const [secret, setSecret] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const [oidcProviders, setOidcProviders] = useState([]);

  // If already authed, bounce.
  useEffect(() => {
    if (session) nav(next, { replace: true });
  }, [session, next, nav]);

  // Discover any configured OIDC providers so we can render "Sign in with..."
  // buttons. Failures are swallowed — falling back to credential-only login
  // is fine. Accepts both raw array and {providers: [...]} wrappers.
  useEffect(() => {
    catalogApi.oidcProviders()
      .then((list) => {
        const arr = Array.isArray(list) ? list : Array.isArray(list?.providers) ? list.providers : [];
        setOidcProviders(arr);
      })
      .catch(() => setOidcProviders([]));
  }, []);

  const parsed = useMemo(() => {
    if (mode === 'combined') {
      const c = combined.trim();
      const i = c.indexOf(':');
      if (i <= 0) return { id: '', sec: '' };
      return { id: c.slice(0, i), sec: c.slice(i + 1) };
    }
    return { id: tokenId.trim(), sec: secret };
  }, [mode, combined, tokenId, secret]);

  const canSubmit = parsed.id && parsed.sec && !busy;

  const onSubmit = async (e) => {
    e.preventDefault();
    if (!canSubmit) return;
    setErr(null);
    setBusy(true);
    try {
      await login(parsed.id, parsed.sec);
      nav(next, { replace: true });
    } catch (e2) { setErr(e2); }
    finally { setBusy(false); }
  };

  if (session === undefined) {
    return <div className="min-h-screen flex items-center justify-center bg-g-canvas"><Spinner size="lg" /></div>;
  }

  return (
    <div className="min-h-screen bg-g-canvas flex flex-col">
      <TopBar area="catalog" linkBrand={false} rightSlot={<ThemeToggle />} />

      <main className="flex-1 flex items-center justify-center px-6 py-12">
        <div className="w-full max-w-md">
          <div className="text-center mb-6">
            <h1 className="text-2xl font-semibold tracking-tight text-g-text">Sign in to your catalog</h1>
            <p className="mt-2 text-sm text-g-text-secondary">
              Use the customer credential issued to your organization. It's the same
              credential you use for <code className="px-1 py-0.5 bg-g-secondary rounded text-xs">docker login</code>.
            </p>
          </div>

          <div className="bg-g-elevated border border-g-border-weak rounded shadow-z1 p-5">
            <div className="flex items-center justify-between mb-4">
              <div className="inline-flex rounded border border-g-border-weak overflow-hidden text-xs">
                <button
                  type="button"
                  onClick={() => setMode('combined')}
                  className={`px-2.5 py-1 ${mode === 'combined' ? 'bg-g-accent-main text-white' : 'bg-g-secondary text-g-text-secondary hover:bg-g-hover'}`}
                >Single paste</button>
                <button
                  type="button"
                  onClick={() => setMode('split')}
                  className={`px-2.5 py-1 ${mode === 'split' ? 'bg-g-accent-main text-white' : 'bg-g-secondary text-g-text-secondary hover:bg-g-hover'}`}
                >Split fields</button>
              </div>
            </div>

            <form onSubmit={onSubmit} className="space-y-3">
              {mode === 'combined' ? (
                <div>
                  <label className="label">Token (id:secret)</label>
                  <input
                    className="input font-mono"
                    autoFocus
                    placeholder="ag_xxxxxxxx:xxxxxxxxxxxxxxxx"
                    value={combined}
                    onChange={(e) => setCombined(e.target.value)}
                    autoComplete="off"
                    spellCheck="false"
                  />
                  <div className="text-xs text-g-text-disabled mt-1">
                    Paste the full <code className="font-mono">id:secret</code> issued in your customer console.
                  </div>
                </div>
              ) : (
                <>
                  <div>
                    <label className="label">Token ID</label>
                    <input
                      className="input font-mono"
                      autoFocus
                      value={tokenId}
                      onChange={(e) => setTokenId(e.target.value)}
                      autoComplete="username"
                      placeholder="ag_xxxxxxxx"
                    />
                  </div>
                  <div>
                    <label className="label">Secret</label>
                    <input
                      type="password"
                      className="input font-mono"
                      value={secret}
                      onChange={(e) => setSecret(e.target.value)}
                      autoComplete="current-password"
                      placeholder="••••••••••••"
                    />
                  </div>
                </>
              )}

              <ErrorBanner error={err} />

              <button
                type="submit"
                className="btn-primary w-full justify-center"
                disabled={!canSubmit}
              >
                {busy ? 'Signing in…' : (<><MdLogin /> Sign in</>)}
              </button>
            </form>

            {oidcProviders.length > 0 && (
              <>
                {/* Default provider (if any) rendered above the divider with primary styling */}
                {oidcProviders.filter((p) => p.is_default).map((p) => (
                  <div key={p.name} className="mt-4">
                    <a
                      href={catalogApi.oidcStartUrl(p.name, next)}
                      className="btn-primary flex items-center justify-center gap-2 w-full px-3 py-2 rounded text-sm"
                    >
                      Sign in with {p.display_name || p.name}
                    </a>
                  </div>
                ))}

                {/* Non-default providers below the divider */}
                {oidcProviders.filter((p) => !p.is_default).length > 0 && (
                  <>
                    <div className="my-4 flex items-center gap-3 text-[10px] uppercase tracking-wider text-g-text-disabled">
                      <span className="flex-1 h-px bg-g-border-weak" />
                      or
                      <span className="flex-1 h-px bg-g-border-weak" />
                    </div>
                    <div className="space-y-2">
                      {oidcProviders.filter((p) => !p.is_default).map((p) => (
                        <a
                          key={p.name}
                          href={catalogApi.oidcStartUrl(p.name, next)}
                          className="flex items-center justify-center gap-2 w-full px-3 py-2 border border-g-border-weak rounded text-sm hover:bg-g-hover transition-colors"
                        >
                          Sign in with {p.display_name || p.name}
                        </a>
                      ))}
                    </div>
                  </>
                )}

              </>
            )}
          </div>

          <div className="mt-6 text-center text-xs text-g-text-secondary">
            Lost your credential? <a href={`mailto:${brand.supportEmail}`} className="text-g-text-link">Contact support</a>.
          </div>
          <div className="mt-2 text-center text-xs text-g-text-disabled">
            Administrator? <Link to="/admin/login" className="text-g-text-link">Admin login</Link>.
          </div>
        </div>
      </main>

      <footer className="text-center py-5 text-xs text-g-text-disabled">
        {brand.footerTagline}
      </footer>
    </div>
  );
}
