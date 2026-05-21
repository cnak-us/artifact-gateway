import { useEffect } from 'react';
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom';
import { MdLogout, MdKey, MdAdminPanelSettings, MdArrowBack } from 'react-icons/md';
import { useCatalogAuth } from '../../contexts/CatalogAuthContext.jsx';
import Spinner from '../../components/Spinner.jsx';
import TopBar from '../../components/TopBar.jsx';
import ThemeToggle from '../../components/ThemeToggle.jsx';
import { brand } from '../../brand/index.js';
import { admin } from '../../api/client.js';

// Customer-facing chrome: shares the same TopBar component as /admin/*
// so the brand block, height, padding and border are pixel-identical
// across the two surfaces. Only the right-side controls differ.
export default function CatalogLayout() {
  const nav = useNavigate();
  const loc = useLocation();
  const { session, logout } = useCatalogAuth();

  useEffect(() => {
    if (session === null) {
      const next = encodeURIComponent(loc.pathname + loc.search);
      nav(`/login?next=${next}`, { replace: true });
    }
  }, [session, loc.pathname, loc.search, nav]);

  if (session === undefined) {
    return <div className="min-h-screen flex items-center justify-center bg-g-canvas"><Spinner size="lg" /></div>;
  }
  if (!session) return null;

  const doLogout = async () => {
    await logout();
    nav('/catalog/login', { replace: true });
  };

  const customer = session.license?.customer || session.customer || 'Customer';
  const impersonator = session.impersonator;
  // canAdmin is set by the Dex auto-flow when the same Dex identity has an
  // admin session minted alongside the customer cookie. Shows the "Admin"
  // shortcut in the top bar — clicking just navigates to /admin since the
  // ag_admin_session cookie is already in the browser.
  const canAdmin = session.can_admin;

  // Admin "view as customer" exit. Clears only ag_customer_session (admin
  // session stays alive), then sends the browser to /admin/licenses where the
  // view-as action lives. Full-page navigation so the SPA picks up the new
  // cookie state cleanly.
  const exitImpersonation = async () => {
    try { await admin.endImpersonation(); } catch { /* best effort */ }
    window.location.assign('/admin/licenses');
  };

  const rightSlot = (
    <>
      <ThemeToggle />
      {canAdmin && (
        <a
          href="/admin"
          className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded font-medium text-sm text-g-text-secondary hover:text-g-text hover:bg-g-hover transition-colors"
          title="Switch to admin console"
        >
          <MdAdminPanelSettings className="text-base" />
          <span className="hidden sm:inline">Admin</span>
        </a>
      )}
      <NavLink
        to="/catalog/credentials"
        className={({ isActive }) =>
          `inline-flex items-center gap-1.5 px-3 py-1.5 rounded font-medium transition-colors ${
            isActive
              ? 'text-g-accent-text bg-g-accent-main/10'
              : 'text-g-text-secondary hover:text-g-text hover:bg-g-hover'
          }`
        }
      >
        <MdKey className="text-base" />
        <span className="hidden sm:inline">My credential</span>
      </NavLink>
      <button
        type="button"
        onClick={doLogout}
        className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded font-medium text-sm text-g-text-secondary hover:text-g-text hover:bg-g-hover transition-colors"
      >
        <MdLogout className="text-base" />
        <span className="hidden sm:inline">Sign out</span>
      </button>
    </>
  );

  return (
    <div className="min-h-screen flex flex-col bg-g-canvas text-g-text">
      <TopBar area="catalog" subtitle={customer} rightSlot={rightSlot} />

      {impersonator && (
        <div className="bg-amber-500/15 border-b border-amber-500/40 text-amber-900 dark:text-amber-200 px-4 py-2 text-sm flex items-center justify-between gap-4">
          <div className="flex items-center gap-2 min-w-0">
            <MdAdminPanelSettings className="text-base shrink-0" />
            <span className="truncate">
              Viewing catalog as <span className="font-medium">{customer}</span>
              {' '}— admin <span className="font-mono">{impersonator}</span>
            </span>
          </div>
          <button
            type="button"
            onClick={exitImpersonation}
            className="inline-flex items-center gap-1 px-2.5 py-1 rounded font-medium text-xs bg-amber-500/20 hover:bg-amber-500/30 transition-colors whitespace-nowrap"
          >
            <MdArrowBack className="text-sm" />
            Return to admin
          </button>
        </div>
      )}

      <main className="flex-1">
        <Outlet />
      </main>

      <footer className="text-center py-5 text-xs text-g-text-disabled border-t border-g-border-weak">
        {brand.footerTagline}
      </footer>
    </div>
  );
}
