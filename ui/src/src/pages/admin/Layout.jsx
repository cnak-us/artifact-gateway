import { useCallback, useEffect, useState } from 'react';
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom';
import clsx from 'clsx';
import {
  MdInventory2, MdGroup, MdVpnKey, MdLockOutline,
  MdVerifiedUser, MdListAlt, MdLogout, MdSettings, MdShowChart,
  MdSearch, MdStorefront,
} from 'react-icons/md';
import { useAuth } from '../../contexts/AuthContext.jsx';
import Spinner from '../../components/Spinner.jsx';
import Badge from '../../components/Badge.jsx';
import Button from '../../components/Button.jsx';
import TopBar from '../../components/TopBar.jsx';
import ThemeToggle from '../../components/ThemeToggle.jsx';
import CommandPalette from '../../components/CommandPalette.jsx';

// Two logical groups separated visually: day-to-day operations on top, system
// and observability on the bottom. Root keys live as a tab inside Licenses, so
// they intentionally don't get a sidebar entry.
const NAV_GROUPS = [
  {
    label: 'Operations',
    items: [
      { to: '/admin/packages',             label: 'Content',     icon: MdInventory2 },
      { to: '/admin/upstream-credentials', label: 'Credentials', icon: MdLockOutline },
      { to: '/admin/customers',            label: 'Customers',   icon: MdGroup },
      { to: '/admin/licenses',             label: 'Licenses',    icon: MdVpnKey },
    ],
  },
  {
    label: 'System',
    items: [
      { to: '/admin/oidc',       label: 'OIDC',          icon: MdVerifiedUser },
      { to: '/admin/audit',      label: 'Audit',         icon: MdListAlt },
      { to: '/admin/monitoring', label: 'Monitoring',    icon: MdShowChart },
      { to: '/admin/config',     label: 'Configuration', icon: MdSettings },
    ],
  },
];

export default function AdminLayout() {
  const nav = useNavigate();
  const loc = useLocation();
  const { adminUser, refreshAdmin, adminLogout } = useAuth();
  const [paletteOpen, setPaletteOpen] = useState(false);

  useEffect(() => { refreshAdmin(); }, [refreshAdmin]);

  useEffect(() => {
    if (adminUser === null) {
      const next = encodeURIComponent(loc.pathname + loc.search);
      nav(`/login?next=${next}`, { replace: true });
    }
  }, [adminUser, loc.pathname, loc.search, nav]);

  // Cmd/Ctrl+K opens the command palette from anywhere inside /admin/*.
  // Listener is bound at the layout level so it's automatically scoped to
  // authenticated admin sessions (Layout returns null otherwise).
  const onKey = useCallback((e) => {
    const isK = e.key && e.key.toLowerCase() === 'k';
    if ((e.metaKey || e.ctrlKey) && !e.shiftKey && !e.altKey && isK) {
      e.preventDefault();
      setPaletteOpen((p) => !p);
    }
  }, []);

  useEffect(() => {
    if (!adminUser) return undefined;
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [adminUser, onKey]);

  if (adminUser === undefined) {
    return <div className="min-h-screen flex items-center justify-center bg-g-canvas"><Spinner label="Loading" /></div>;
  }
  if (!adminUser) return null;

  const logout = async () => {
    await adminLogout();
    nav('/admin/login', { replace: true });
  };

  const rightSlot = (
    <>
      <button
        type="button"
        onClick={() => setPaletteOpen(true)}
        title="Command palette (⌘K)"
        className="hidden md:inline-flex items-center gap-2 px-2.5 py-1.5 rounded border border-g-border-weak text-g-text-secondary hover:text-g-text hover:bg-g-hover transition-colors text-xs"
      >
        <MdSearch className="text-sm" />
        <span>Search…</span>
        <kbd className="px-1 py-0.5 rounded bg-g-secondary border border-g-border-weak text-[10px] leading-none">⌘K</kbd>
      </button>
      <ThemeToggle />
      {adminUser.can_customer && (
        <a
          href="/catalog"
          className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded font-medium text-sm text-g-text-secondary hover:text-g-text hover:bg-g-hover transition-colors"
          title="Switch to catalog view"
        >
          <MdStorefront className="text-base" />
          <span className="hidden sm:inline">Catalog</span>
        </a>
      )}
      <div className="hidden sm:block text-g-text">{adminUser.email}</div>
      <Badge color="gray">{adminUser.role}</Badge>
      <Button variant="outline" size="sm" icon={<MdLogout />} onClick={logout}>Logout</Button>
    </>
  );

  return (
    <div className="min-h-screen flex flex-col bg-g-canvas">
      <TopBar area="admin" rightSlot={rightSlot} />

      <div className="flex-1 flex min-h-0">
        <aside className="w-60 shrink-0 bg-g-primary border-r border-g-border-weak flex flex-col">
          <nav className="flex-1 p-2 text-sm overflow-y-auto">
            {NAV_GROUPS.map((group, gi) => (
              <div key={group.label} className={gi > 0 ? 'mt-4 pt-4 border-t border-g-border-weak' : ''}>
                <div className="px-3 mb-1 text-[10px] font-semibold uppercase tracking-wider text-g-text-disabled">
                  {group.label}
                </div>
                <div className="space-y-0.5">
                  {group.items.map((item) => {
                    const Icon = item.icon;
                    return (
                      <NavLink
                        key={item.to}
                        to={item.to}
                        className={({ isActive }) =>
                          clsx(
                            'flex items-center gap-2 px-3 py-2 rounded transition-colors',
                            isActive
                              ? 'bg-g-accent-main text-white'
                              : 'text-g-text-secondary hover:bg-g-hover hover:text-g-text',
                          )
                        }
                      >
                        <Icon className="text-base" />
                        {item.label}
                      </NavLink>
                    );
                  })}
                </div>
              </div>
            ))}
          </nav>
        </aside>

        <main className="flex-1 overflow-auto p-6 min-w-0">
          <Outlet />
        </main>
      </div>

      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        adminLogout={adminLogout}
      />
    </div>
  );
}
