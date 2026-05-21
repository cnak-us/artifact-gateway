import { useEffect, useMemo, useState } from 'react';
import { MdAdd, MdDelete, MdWarning, MdGroup } from 'react-icons/md';
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
import Input from '../../components/Input.jsx';
import Select from '../../components/Select.jsx';
import Card from '../../components/Card.jsx';
import CopyableCode from '../../components/CopyableCode.jsx';

export default function Customers() {
  const toast = useToast();
  const confirm = useConfirm();
  const [tokens, setTokens] = useState(null);
  const [licenses, setLicenses] = useState([]);
  const [err, setErr] = useState(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [newCred, setNewCred] = useState(null);

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

  const grouped = useMemo(() => {
    if (!tokens) return [];
    const byLic = new Map();
    for (const t of tokens) {
      if (!byLic.has(t.license_id)) byLic.set(t.license_id, []);
      byLic.get(t.license_id).push(t);
    }
    return [...byLic.entries()].map(([licId, ts]) => ({
      license: licenses.find((l) => l.id === licId) || { id: licId, license_id: licId },
      tokens: ts,
    }));
  }, [tokens, licenses]);

  const revoke = async (t) => {
    const ok = await confirm({
      title: 'Revoke token?',
      message: `Token ${t.token_id} will be revoked. The customer will be locked out immediately.`,
      confirmLabel: 'Revoke token',
      danger: true,
    });
    if (!ok) return;
    try { await admin.deleteCustomerToken(t.id); toast.success('Token revoked'); await load(); }
    catch (e) { toast.error(e.message); }
  };

  const tokenColumns = [
    { key: 'token_id', header: 'Token ID', render: (t) => <span className="font-mono text-xs">{t.token_id}</span> },
    { key: 'description', header: 'Description', render: (t) => t.description || '—' },
    { key: 'expires', header: 'Expires', render: (t) => <span className="text-xs">{t.expires_at ? new Date(t.expires_at).toLocaleString() : 'never'}</span> },
    { key: 'last_used', header: 'Last used', render: (t) => <span className="text-xs text-g-text-secondary">{t.last_used_at ? new Date(t.last_used_at).toLocaleString() : 'never'}</span> },
    {
      key: 'status',
      header: 'Status',
      render: (t) => t.revoked_at ? <Badge color="red">revoked</Badge> : <Badge color="green">active</Badge>,
    },
    {
      key: 'actions',
      header: '',
      className: 'text-right',
      render: (t) => !t.revoked_at ? (
        <IconButton icon={<MdDelete />} label="Revoke" variant="danger" onClick={() => revoke(t)} />
      ) : null,
    },
  ];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Customers</h1>
          <p className="text-sm text-g-text-secondary">Per-customer credentials bound to a license.</p>
        </div>
        <Button variant="primary" icon={<MdAdd />} onClick={() => setCreateOpen(true)}>Generate token</Button>
      </div>

      <ErrorBanner error={err} />

      {tokens === null ? <Spinner label="Loading tokens" /> : grouped.length === 0 ? (
        <EmptyState
          icon={MdGroup}
          title="No customer tokens yet"
          description="Generate a credential bound to a license. The customer uses it to `docker pull` and to log into the catalog."
          action={<Button variant="primary" icon={<MdAdd />} onClick={() => setCreateOpen(true)}>Generate token</Button>}
        />
      ) : (
        <div className="space-y-4">
          {grouped.map(({ license, tokens }) => (
            <Card key={license.id} padding="none">
              <div className="px-4 py-2.5 border-b border-g-border-weak bg-g-secondary/50">
                <div className="font-medium text-sm">{license.customer || license.license_id}</div>
                <div className="text-xs text-g-text-secondary flex items-center gap-2 mt-0.5">
                  <span className="font-mono">{license.license_id}</span>
                  {license.tier && <Badge color="blue">{license.tier}</Badge>}
                </div>
              </div>
              <Table columns={tokenColumns} rows={tokens} className="border-0 rounded-none" />
            </Card>
          ))}
        </div>
      )}

      <GenerateModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        licenses={licenses}
        onCreated={(result) => { setCreateOpen(false); setNewCred(result); load(); }}
      />
      <NewCredentialModal cred={newCred} onClose={() => setNewCred(null)} />
    </div>
  );
}

function GenerateModal({ open, onClose, licenses, onCreated }) {
  const [form, setForm] = useState({ license_id: '', description: '', expires_at: '' });
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) { setForm({ license_id: '', description: '', expires_at: '' }); setErr(null); }
  }, [open]);

  const set = (k) => (e) => setForm((f) => ({ ...f, [k]: e.target.value }));

  const save = async () => {
    setErr(null); setBusy(true);
    try {
      const result = await admin.createCustomerToken({
        license_id: form.license_id,
        description: form.description || undefined,
        expires_at: form.expires_at ? new Date(form.expires_at).toISOString() : undefined,
      });
      onCreated(result);
    } catch (e) { setErr(e); }
    finally { setBusy(false); }
  };

  const licOpts = licenses.filter((l) => !l.revoked_at).map((l) => ({
    value: l.id,
    label: `${l.customer || l.license_id} — ${l.license_id}`,
  }));

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Generate customer token"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!form.license_id} onClick={save}>{busy ? 'Generating…' : 'Generate'}</Button>
        </>
      }
    >
      <div className="space-y-3">
        <Select label="License *" value={form.license_id} onChange={set('license_id')} placeholder="— select license —" options={licOpts} />
        <Input label="Description" value={form.description} onChange={set('description')} placeholder="e.g. CI runner — east-cluster" />
        <Input label="Expires at (optional)" type="datetime-local" value={form.expires_at} onChange={set('expires_at')} />
        <ErrorBanner error={err} />
      </div>
    </Modal>
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
