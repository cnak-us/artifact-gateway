import { useState, useEffect, useRef, useCallback, useMemo } from 'react';
import { createPortal } from 'react-dom';
import { useNavigate } from 'react-router-dom';
import clsx from 'clsx';
import {
  MdSearch, MdClose,
  MdInventory2, MdGroup, MdVpnKey, MdLockOutline,
  MdVerifiedUser, MdListAlt, MdSettings, MdShowChart,
  MdLogout, MdRefresh, MdKey, MdTune,
} from 'react-icons/md';

// Command palette for the artifact-gateway admin UI. Triggered by Cmd/Ctrl+K
// from any /admin/* route (wired in pages/admin/Layout.jsx).
//
// Sections:
//   - Pages           navigation to top-level admin routes
//   - Deep links      sub-tabs that live behind ?tab= / #hash on existing pages
//                     (Licenses → Root keys, Config → State / Customization)
//   - Actions         in-app commands (Reload, Logout)
//
// Each command carries a `keywords` bag so admins find things by what they
// actually type (e.g. "oidc" → OIDC, "wipe" → not yet, "rotate" → Root keys).
// New commands: append to the matching group below.

const GROUPS = {
  PAGES: 'Pages',
  DEEP:  'Deep links',
  ACTIONS: 'Actions',
};

const PAGE_COMMANDS = [
  { id: 'p-packages',     label: 'Content',         to: '/admin/packages',             icon: MdInventory2,  keywords: ['content', 'packages', 'artifacts', 'oci', 'images', 'helm', 'charts', 'binaries'] },
  { id: 'p-credentials',  label: 'Upstream Credentials', to: '/admin/upstream-credentials', icon: MdLockOutline, keywords: ['credentials', 'upstream', 'registry', 'pull', 'secrets', 'creds'] },
  { id: 'p-customers',    label: 'Customers',       to: '/admin/customers',            icon: MdGroup,       keywords: ['customers', 'tenants', 'orgs', 'accounts'] },
  { id: 'p-licenses',     label: 'Licenses',        to: '/admin/licenses',             icon: MdVpnKey,      keywords: ['licenses', 'entitlements', 'tiers', 'expiry', 'issued'] },
  { id: 'p-oidc',         label: 'OIDC',            to: '/admin/oidc',                 icon: MdVerifiedUser,keywords: ['oidc', 'sso', 'oauth', 'login', 'identity', 'providers', 'dex'] },
  { id: 'p-audit',        label: 'Audit',           to: '/admin/audit',                icon: MdListAlt,     keywords: ['audit', 'log', 'trail', 'security', 'events', 'history'] },
  { id: 'p-monitoring',   label: 'Monitoring',      to: '/admin/monitoring',           icon: MdShowChart,   keywords: ['monitoring', 'metrics', 'charts', 'graphs', 'observability', 'prometheus'] },
  { id: 'p-config',       label: 'Configuration',   to: '/admin/config',               icon: MdSettings,    keywords: ['config', 'configuration', 'settings', 'state', 'apply'] },
];

const DEEP_COMMANDS = [
  { id: 'd-root-keys',     label: 'Licenses → Root keys',     to: '/admin/licenses?tab=root-keys', icon: MdKey,
    keywords: ['root', 'keys', 'signing', 'rotate', 'jwks', 'issuer', 'crypto'] },
  { id: 'd-config-state',  label: 'Configuration → State',    to: '/admin/config#state',           icon: MdSettings,
    keywords: ['state', 'config', 'declarative', 'apply', 'yaml'] },
  { id: 'd-config-custom', label: 'Configuration → Customization', to: '/admin/config#customization', icon: MdTune,
    keywords: ['customization', 'branding', 'theme', 'logo', 'colors', 'tagline'] },
];

// Built lazily — needs access to navigate + adminLogout from props.
function buildActionCommands({ adminLogout, navigate }) {
  return [
    { id: 'a-reload', label: 'Reload UI', icon: MdRefresh, keywords: ['reload', 'refresh', 'reset', 'restart'],
      run: () => window.location.reload() },
    { id: 'a-logout', label: 'Logout',    icon: MdLogout,  keywords: ['logout', 'signout', 'sign out', 'lock', 'exit'],
      run: async () => { await adminLogout(); navigate('/admin/login', { replace: true }); } },
  ];
}

export default function CommandPalette({ open, onClose, adminLogout }) {
  const navigate = useNavigate();
  const [query, setQuery] = useState('');
  const [selectedIndex, setSelectedIndex] = useState(0);
  const inputRef = useRef(null);
  const listRef = useRef(null);

  const actionCommands = useMemo(
    () => buildActionCommands({ adminLogout, navigate }),
    [adminLogout, navigate],
  );

  const matches = useCallback((cmd, q) => {
    if (!q) return true;
    const label = cmd.label.toLowerCase();
    if (label.includes(q)) return true;
    if (cmd.to && cmd.to.toLowerCase().includes(q)) return true;
    return cmd.keywords?.some((kw) => kw.includes(q));
  }, []);

  const q = query.trim().toLowerCase();
  const filteredPages   = useMemo(() => PAGE_COMMANDS.filter((c) => matches(c, q)),   [q, matches]);
  const filteredDeep    = useMemo(() => DEEP_COMMANDS.filter((c) => matches(c, q)),   [q, matches]);
  const filteredActions = useMemo(() => actionCommands.filter((c) => matches(c, q)), [q, matches, actionCommands]);

  // Flat list drives keyboard navigation; section dividers are visual only.
  const flatItems = useMemo(
    () => [...filteredPages, ...filteredDeep, ...filteredActions],
    [filteredPages, filteredDeep, filteredActions],
  );
  const totalItems = flatItems.length;

  useEffect(() => {
    if (open) {
      setQuery('');
      setSelectedIndex(0);
      // setTimeout so the modal is in the DOM before we focus the input.
      const id = setTimeout(() => inputRef.current?.focus(), 30);
      return () => clearTimeout(id);
    }
  }, [open]);

  useEffect(() => {
    if (selectedIndex >= totalItems) {
      setSelectedIndex(Math.max(0, totalItems - 1));
    }
  }, [totalItems, selectedIndex]);

  useEffect(() => {
    if (!listRef.current) return;
    const el = listRef.current.querySelector(`[data-cmd-index="${selectedIndex}"]`);
    if (el) el.scrollIntoView({ block: 'nearest' });
  }, [selectedIndex]);

  const runCommand = useCallback((cmd) => {
    if (!cmd) return;
    if (cmd.run) {
      cmd.run();
    } else if (cmd.to) {
      // react-router-dom doesn't navigate to hash-only URLs by default; split
      // and let the browser scroll-into-hash on top of the route push.
      navigate(cmd.to);
    }
    onClose();
  }, [navigate, onClose]);

  const handleKeyDown = useCallback((e) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (totalItems === 0) return;
      setSelectedIndex((prev) => (prev + 1) % totalItems);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      if (totalItems === 0) return;
      setSelectedIndex((prev) => (prev - 1 + totalItems) % totalItems);
    } else if (e.key === 'Enter') {
      e.preventDefault();
      runCommand(flatItems[selectedIndex]);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      onClose();
    }
  }, [totalItems, selectedIndex, flatItems, runCommand, onClose]);

  if (!open) return null;

  // selectedIndex maps onto the flat list; section-relative offsets let each
  // section render the right highlight without recomputing the global index.
  let cursor = 0;
  const pagesStart   = cursor; cursor += filteredPages.length;
  const deepStart    = cursor; cursor += filteredDeep.length;
  const actionsStart = cursor; cursor += filteredActions.length;

  const renderCommand = (cmd, flatIndex) => {
    const Icon = cmd.icon;
    const isSelected = flatIndex === selectedIndex;
    return (
      <button
        key={cmd.id}
        type="button"
        data-cmd-index={flatIndex}
        onClick={() => runCommand(cmd)}
        onMouseEnter={() => setSelectedIndex(flatIndex)}
        className={clsx(
          'w-full flex items-center gap-3 px-4 py-2.5 text-left transition-colors',
          isSelected
            ? 'bg-g-accent-main/10 text-g-text'
            : 'text-g-text-secondary hover:bg-g-hover',
        )}
      >
        {Icon && <Icon className="text-lg w-5 h-5 shrink-0" />}
        <span className="text-sm font-medium truncate">{cmd.label}</span>
        {cmd.to && (
          <span className="ml-auto text-xs text-g-text-disabled truncate max-w-[220px]">
            {cmd.to}
          </span>
        )}
      </button>
    );
  };

  return createPortal(
    <div
      className="fixed inset-0 z-[2000] flex items-start justify-center pt-[15vh] bg-black/60 animate-fadeIn"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        className="w-full max-w-lg bg-g-elevated border border-g-border-weak rounded shadow-z3 overflow-hidden"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-3 px-4 py-3 border-b border-g-border-weak">
          <MdSearch className="text-xl text-g-text-secondary shrink-0" />
          <input
            ref={inputRef}
            type="text"
            value={query}
            onChange={(e) => { setQuery(e.target.value); setSelectedIndex(0); }}
            onKeyDown={handleKeyDown}
            placeholder="Search pages, deep links, actions…"
            className="flex-1 bg-transparent text-g-text text-sm placeholder-g-text-disabled outline-none"
          />
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="p-1 rounded text-g-text-secondary hover:text-g-text hover:bg-g-hover transition-colors"
          >
            <MdClose className="text-lg" />
          </button>
        </div>

        <div ref={listRef} className="max-h-[60vh] overflow-y-auto py-1">
          {filteredPages.length > 0 && (
            <>
              <div className="px-4 py-1.5 text-[10px] font-semibold uppercase tracking-wider text-g-text-disabled">
                {GROUPS.PAGES}
              </div>
              {filteredPages.map((c, i) => renderCommand(c, pagesStart + i))}
            </>
          )}
          {filteredDeep.length > 0 && (
            <>
              {filteredPages.length > 0 && (
                <div className="mx-3 my-1 border-t border-g-border-weak" />
              )}
              <div className="px-4 py-1.5 text-[10px] font-semibold uppercase tracking-wider text-g-text-disabled">
                {GROUPS.DEEP}
              </div>
              {filteredDeep.map((c, i) => renderCommand(c, deepStart + i))}
            </>
          )}
          {filteredActions.length > 0 && (
            <>
              {(filteredPages.length > 0 || filteredDeep.length > 0) && (
                <div className="mx-3 my-1 border-t border-g-border-weak" />
              )}
              <div className="px-4 py-1.5 text-[10px] font-semibold uppercase tracking-wider text-g-text-disabled">
                {GROUPS.ACTIONS}
              </div>
              {filteredActions.map((c, i) => renderCommand(c, actionsStart + i))}
            </>
          )}
          {totalItems === 0 && (
            <div className="px-4 py-6 text-center text-sm text-g-text-disabled">
              No commands match
            </div>
          )}
        </div>

        <div className="px-4 py-2 border-t border-g-border-weak flex items-center gap-4 text-xs text-g-text-disabled">
          <span><kbd className="px-1.5 py-0.5 rounded bg-g-secondary border border-g-border-weak text-[10px]">↑↓</kbd> navigate</span>
          <span><kbd className="px-1.5 py-0.5 rounded bg-g-secondary border border-g-border-weak text-[10px]">Enter</kbd> select</span>
          <span><kbd className="px-1.5 py-0.5 rounded bg-g-secondary border border-g-border-weak text-[10px]">Esc</kbd> close</span>
          <span className="ml-auto"><kbd className="px-1.5 py-0.5 rounded bg-g-secondary border border-g-border-weak text-[10px]">⌘K</kbd></span>
        </div>
      </div>
    </div>,
    document.body,
  );
}
