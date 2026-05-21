import { useEffect, useState } from 'react';
import { MdAdd, MdDelete, MdVerifiedUser } from 'react-icons/md';
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

export default function OIDC() {
  const toast = useToast();
  const confirm = useConfirm();
  const [items, setItems] = useState(null);
  const [err, setErr] = useState(null);
  const [open, setOpen] = useState(false);

  const load = async () => {
    setErr(null);
    try { setItems(await admin.listOIDCProviders() || []); } catch (e) { setErr(e); }
  };
  useEffect(() => { load(); }, []);

  const remove = async (p) => {
    const ok = await confirm({
      title: 'Delete OIDC provider?',
      message: `"${p.name}" will be deleted. Users authenticating through this provider will be signed out.`,
      confirmLabel: 'Delete provider',
      danger: true,
    });
    if (!ok) return;
    try { await admin.deleteOIDCProvider(p.id); toast.success('Provider deleted'); await load(); }
    catch (e) { toast.error(e.message); }
  };

  const columns = [
    { key: 'name', header: 'Name', render: (p) => <span className="font-medium">{p.name}</span> },
    { key: 'issuer', header: 'Issuer', render: (p) => <span className="text-xs font-mono">{p.issuer_url}</span> },
    { key: 'client_id', header: 'Client ID', render: (p) => <span className="text-xs font-mono">{p.client_id}</span> },
    { key: 'scopes', header: 'Scopes', render: (p) => <span className="text-xs">{(p.scopes || []).join(' ')}</span> },
    { key: 'enabled', header: 'Enabled', render: (p) => p.enabled ? <Badge color="green">enabled</Badge> : <Badge color="gray">disabled</Badge> },
    {
      key: 'actions',
      header: '',
      className: 'text-right',
      render: (p) => <IconButton icon={<MdDelete />} label="Delete" variant="danger" onClick={() => remove(p)} />,
    },
  ];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">OIDC providers</h1>
          <p className="text-sm text-g-text-secondary">External identity providers for admin login (Dex, Google, etc).</p>
        </div>
        <Button variant="primary" icon={<MdAdd />} onClick={() => setOpen(true)}>Add provider</Button>
      </div>

      <ErrorBanner error={err} />

      {items === null ? <Spinner label="Loading providers" /> : items.length === 0 ? (
        <EmptyState
          icon={MdVerifiedUser}
          title="No OIDC providers"
          description="Add Dex or another OIDC provider to let admins sign in with SSO. Local password login is always available."
          action={<Button variant="primary" icon={<MdAdd />} onClick={() => setOpen(true)}>Add provider</Button>}
        />
      ) : (
        <Table columns={columns} rows={items} />
      )}

      <CreateModal
        open={open}
        onClose={() => setOpen(false)}
        onSaved={(name) => { setOpen(false); toast.success(`Added "${name}"`); load(); }}
      />
    </div>
  );
}

function CreateModal({ open, onClose, onSaved }) {
  const [form, setForm] = useState({
    name: '', issuer_url: '', client_id: '', client_secret: '',
    scopes: 'openid email profile', enabled: true,
  });
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) {
      setForm({ name: '', issuer_url: '', client_id: '', client_secret: '', scopes: 'openid email profile', enabled: true });
      setErr(null);
    }
  }, [open]);

  const set = (k) => (e) => setForm((f) => ({ ...f, [k]: e.target.type === 'checkbox' ? e.target.checked : e.target.value }));

  const save = async () => {
    setErr(null); setBusy(true);
    try {
      await admin.createOIDCProvider({
        ...form,
        scopes: form.scopes.split(/\s+/).filter(Boolean),
      });
      onSaved(form.name);
    } catch (e) { setErr(e); }
    finally { setBusy(false); }
  };

  const canSave = form.name && form.issuer_url && form.client_id && form.client_secret;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Add OIDC provider"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!canSave} onClick={save}>{busy ? 'Saving…' : 'Save'}</Button>
        </>
      }
    >
      <div className="space-y-3">
        <Input
          label="Name *"
          value={form.name}
          onChange={set('name')}
          placeholder="dex"
          hint="Used in the callback URL: /api/v1/auth/oidc/<name>/callback"
        />
        <Input label="Issuer URL *" value={form.issuer_url} onChange={set('issuer_url')} placeholder="https://dex.example.com" />
        <Input label="Client ID *" value={form.client_id} onChange={set('client_id')} className="font-mono" />
        <Input
          label="Client secret *"
          type="password"
          value={form.client_secret}
          onChange={set('client_secret')}
          hint="Stored AES-GCM encrypted with the server KEK."
          className="font-mono"
        />
        <Input label="Scopes" value={form.scopes} onChange={set('scopes')} placeholder="openid email profile" className="font-mono" />
        <label className="flex items-center gap-2 text-sm text-g-text">
          <input type="checkbox" checked={form.enabled} onChange={set('enabled')} />
          Enable on the login page
        </label>
        <ErrorBanner error={err} />
      </div>
    </Modal>
  );
}
