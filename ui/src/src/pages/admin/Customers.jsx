import { useEffect, useMemo, useState } from 'react';
import { MdAdd, MdRefresh, MdDelete, MdWarning, MdGroup, MdExpandMore, MdExpandLess } from 'react-icons/md';
import { admin } from '../../api/client.js';
import { useToast } from '../../components/Toast.jsx';
import { useConfirm } from '../../components/ConfirmDialog.jsx';
import Button from '../../components/Button.jsx';
import IconButton from '../../components/IconButton.jsx';
import Modal from '../../components/Modal.jsx';
import Spinner from '../../components/Spinner.jsx';
import ErrorBanner from '../../components/ErrorBanner.jsx';
import Table from '../../components/Table.jsx';
import Badge from '../../components/Badge.jsx';
import EmptyState from '../../components/EmptyState.jsx';
import Card from '../../components/Card.jsx';
import CopyableCode from '../../components/CopyableCode.jsx';

// Customers shows one row per license with its single active customer
// credential (if any) plus a collapsible history of revoked tokens. The
// invariant "one active credential per license" is enforced by the backend
// partial unique index; this page only ever shows the active token in the
// primary row and rotation replaces it atomically.
export default function Customers() {
  const toast = useToast();
  const confirm = useConfirm();
  const [tokens, setTokens] = useState(null);
  const [licenses, setLicenses] = useState([]);
  const [err, setErr] = useState(null);
  const [newCred, setNewCred] = useState(null);
  const [busyLic, setBusyLic] = useState(null); // license id currently rotating

  const load = async () => {
    setErr(null);
    try {
      const [ts, ls] = await Promise.all([
        admin.listCustomerTokens(),
        admin.listLicenses(),
      ]);
      setTokens(ts || []);
      setLicenses(ls || []);
    } catch (e) { setErr(e); }
  };
  useEffect(() => { load(); }, []);

  // One row per license: a primary "active token" slot (or "no credential")
  // and a list of revoked tokens for forensics. Licenses without any tokens
  // still appear so admins can issue a first credential.
  const rows = useMemo(() => {
    if (!tokens) return [];
    const tokensByLic = new Map();
    for (const t of tokens) {
      if (!tokensByLic.has(t.license_id)) tokensByLic.set(t.license_id, []);
      tokensByLic.get(t.license_id).push(t);
    }
    return licenses
      .filter((l) => !l.revoked_at)
      .map((license) => {
        const lt = tokensByLic.get(license.id) || [];
        const active = lt.find((t) => !t.revoked_at) || null;
        const history = lt.filter((t) => t.revoked_at)
          .sort((a, b) => new Date(b.revoked_at) - new Date(a.revoked_at));
        return { license, active, history };
      });
  }, [tokens, licenses]);

  const rotate = async (license) => {
    const ok = await confirm({
      title: license.active ? `Rotate credential for ${license.customer || license.license_id}?` : `Generate credential for ${license.customer || license.license_id}?`,
      message: license.active
        ? 'This revokes the current credential immediately. Pulls using the old secret will start failing within seconds.'
        : 'Issue a new credential. You will see the secret once.',
      confirmLabel: license.active ? 'Rotate' : 'Generate',
      danger: !!license.active,
    });
    if (!ok) return;
    setBusyLic(license.id);
    try {
      const result = await admin.rotateCustomerToken(license.id);
      setNewCred(result);
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      setBusyLic(null);
    }
  };

  // Flip the per-license customer-self-rotate flag. The PATCH endpoint is the
  // only way to change this field (no generic license PUT), and it returns the
  // full updated DTO — we splice that back into the licenses array so the row
  // re-renders from the server-of-record without a full refetch.
  const setCustomerRotateEnabled = async (license, enabled) => {
    try {
      const updated = await admin.updateLicenseCustomerRotate(license.id, enabled);
      setLicenses((prev) => prev.map((l) => (l.id === license.id ? { ...l, ...updated } : l)));
      toast.success(enabled ? 'Customer self-rotation enabled' : 'Customer self-rotation disabled');
    } catch (e) {
      toast.error(e.message);
    }
  };

  // Manual revoke kept as an escape hatch even though "rotate" now replaces
  // it as the everyday action. Useful when an admin wants the license to
  // have NO credential (e.g. customer offboarding).
  const revoke = async (t) => {
    const ok = await confirm({
      title: 'Revoke without replacement?',
      message: `Token ${t.token_id} will be revoked and the license will have no active credential until you generate a new one.`,
      confirmLabel: 'Revoke token',
      danger: true,
    });
    if (!ok) return;
    try { await admin.deleteCustomerToken(t.id); toast.success('Token revoked'); await load(); }
    catch (e) { toast.error(e.message); }
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Customers</h1>
          <p className="text-sm text-g-text-secondary">
            One docker-pull credential per license. Rotate to issue a new secret;
            the previous credential is revoked atomically.
          </p>
        </div>
      </div>

      <ErrorBanner error={err} />

      {tokens === null ? (
        <Spinner label="Loading credentials" />
      ) : rows.length === 0 ? (
        <EmptyState
          icon={MdGroup}
          title="No licenses yet"
          description="Issue a license first; each license gets its own credential."
        />
      ) : (
        <div className="space-y-4">
          {rows.map(({ license, active, history }) => (
            <LicenseCredentialRow
              key={license.id}
              license={{ ...license, active }}
              active={active}
              history={history}
              busy={busyLic === license.id}
              onRotate={() => rotate({ ...license, active })}
              onRevoke={() => active && revoke(active)}
              onToggleCustomerRotate={(enabled) => setCustomerRotateEnabled(license, enabled)}
            />
          ))}
        </div>
      )}

      <NewCredentialModal cred={newCred} onClose={() => setNewCred(null)} />
    </div>
  );
}

function LicenseCredentialRow({ license, active, history, busy, onRotate, onRevoke, onToggleCustomerRotate }) {
  const [showHistory, setShowHistory] = useState(false);
  const [rotateBusy, setRotateBusy] = useState(false);
  // Default to true for back-compat with rows that haven't been migrated /
  // licenses where the field was added after creation; the backend defaults
  // existing rows to true so this matches the server contract.
  const customerRotateEnabled = license.customer_rotate_enabled !== false;

  const handleToggle = async (e) => {
    const next = e.target.checked;
    setRotateBusy(true);
    try { await onToggleCustomerRotate(next); }
    finally { setRotateBusy(false); }
  };

  const historyColumns = [
    { key: 'token_id', header: 'Token ID', render: (t) => <span className="font-mono text-xs">{t.token_id}</span> },
    { key: 'description', header: 'Description', render: (t) => t.description || '—' },
    { key: 'created', header: 'Created', render: (t) => <span className="text-xs">{t.created_at ? new Date(t.created_at).toLocaleString() : '—'}</span> },
    { key: 'revoked', header: 'Revoked', render: (t) => <span className="text-xs text-g-text-secondary">{t.revoked_at ? new Date(t.revoked_at).toLocaleString() : '—'}</span> },
    { key: 'last_used', header: 'Last used', render: (t) => <span className="text-xs text-g-text-secondary">{t.last_used_at ? new Date(t.last_used_at).toLocaleString() : 'never'}</span> },
  ];

  return (
    <Card padding="none">
      <div className="px-4 py-2.5 border-b border-g-border-weak bg-g-secondary/50 flex items-center justify-between gap-3 flex-wrap">
        <div className="min-w-0">
          <div className="font-medium text-sm">{license.customer || license.license_id}</div>
          <div className="text-xs text-g-text-secondary flex items-center gap-2 mt-0.5">
            <span className="font-mono">{license.license_id}</span>
            {license.tier && <Badge color="blue">{license.tier}</Badge>}
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant={active ? 'ghost' : 'primary'}
            icon={active ? <MdRefresh /> : <MdAdd />}
            onClick={onRotate}
            disabled={busy}
          >
            {busy ? (active ? 'Rotating…' : 'Generating…') : (active ? 'Rotate' : 'Generate token')}
          </Button>
          {active && (
            <Button variant="danger" icon={<MdDelete />} onClick={onRevoke} disabled={busy}>
              Revoke without replacement
            </Button>
          )}
        </div>
      </div>

      <div className="px-4 py-3 text-sm">
        {active ? (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-x-6 gap-y-1.5">
            <Row label="Token ID"    value={<code className="font-mono text-xs">{active.token_id}</code>} />
            <Row label="Status"      value={<Badge color="green">active</Badge>} />
            <Row label="Created"     value={active.created_at ? new Date(active.created_at).toLocaleString() : '—'} />
            <Row label="Expires"     value={active.expires_at ? new Date(active.expires_at).toLocaleString() : 'never'} />
            <Row label="Last used"   value={active.last_used_at ? new Date(active.last_used_at).toLocaleString() : 'never'} />
            <Row label="Description" value={active.description || '—'} />
          </div>
        ) : (
          <p className="text-g-text-secondary">No active credential.</p>
        )}
      </div>

      <div className="px-4 py-3 border-t border-g-border-weak">
        <label className="flex items-center gap-2 text-sm text-g-text select-none">
          <input
            type="checkbox"
            checked={customerRotateEnabled}
            onChange={handleToggle}
            disabled={rotateBusy}
            className="rounded border-g-border-medium bg-g-secondary text-g-accent-main focus:ring-g-accent-main/40"
          />
          <span>Allow customer to rotate their own credential</span>
        </label>
        <p className="mt-1 ml-6 text-xs text-g-text-secondary">
          When off, only an admin can rotate this license's credential. Useful for shared demo licenses.
        </p>
      </div>

      {history.length > 0 && (
        <div className="border-t border-g-border-weak">
          <button
            type="button"
            onClick={() => setShowHistory((v) => !v)}
            className="w-full px-4 py-2 text-left text-xs font-medium text-g-text-secondary hover:bg-g-hover transition-colors flex items-center gap-1.5"
          >
            {showHistory ? <MdExpandLess /> : <MdExpandMore />}
            {showHistory ? 'Hide' : 'Show'} revoked credentials ({history.length})
          </button>
          {showHistory && (
            <Table columns={historyColumns} rows={history} className="border-0 rounded-none" />
          )}
        </div>
      )}
    </Card>
  );
}

function Row({ label, value }) {
  return (
    <div className="flex items-baseline gap-3 min-w-0">
      <dt className="w-24 shrink-0 text-xs uppercase tracking-wider font-medium text-g-text-secondary">{label}</dt>
      <dd className="min-w-0 flex-1 text-g-text break-all">{value}</dd>
    </div>
  );
}

function NewCredentialModal({ cred, onClose }) {
  return (
    <Modal
      open={!!cred}
      onClose={onClose}
      title="New customer credential"
      size="lg"
      footer={<Button variant="primary" onClick={onClose}>Done</Button>}
    >
      {cred && (
        <div className="space-y-4">
          <div className="flex items-start gap-2 px-3 py-2.5 border rounded text-sm bg-g-orange-main/10 border-g-orange-main/30 text-g-orange-text">
            <MdWarning className="mt-0.5 shrink-0 text-lg" />
            <div>
              <strong>This is the only time you will see this credential.</strong>
              {' '}Copy it now — the secret is bcrypt-hashed and cannot be recovered.
            </div>
          </div>

          <div>
            <div className="text-xs font-semibold uppercase tracking-wider text-g-text-secondary mb-1.5">Token ID</div>
            <CopyableCode value={cred.token_id} />
          </div>
          <div>
            <div className="text-xs font-semibold uppercase tracking-wider text-g-text-secondary mb-1.5">Secret</div>
            <CopyableCode value={cred.secret} />
          </div>
          <div>
            <div className="text-xs font-semibold uppercase tracking-wider text-g-text-secondary mb-1.5">
              Full credential (for <code className="font-mono">docker login -u :id -p :secret</code>)
            </div>
            <CopyableCode value={cred.full_credential || `${cred.token_id}:${cred.secret}`} />
          </div>
        </div>
      )}
    </Modal>
  );
}
