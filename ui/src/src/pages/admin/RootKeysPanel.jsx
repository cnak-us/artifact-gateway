import { useEffect, useState } from 'react';
import { MdAdd, MdDelete, MdCheckCircle, MdKey, MdRadioButtonChecked } from 'react-icons/md';
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
import Textarea from '../../components/Textarea.jsx';
import CopyableCode from '../../components/CopyableCode.jsx';

const MODE_OPTS = [
  { value: 'generate', label: 'Generate new keypair (server-side)' },
  { value: 'upload',   label: 'Upload existing private key (migrate from cnaklic)' },
];

function cleanHex(s) {
  return (s || '').replace(/\s+/g, '').toLowerCase();
}

function hexLooksValid(s) {
  const c = cleanHex(s);
  return c.length === 128 && /^[0-9a-f]+$/.test(c);
}

export default function RootKeysPanel() {
  const toast = useToast();
  const confirm = useConfirm();
  const [items, setItems] = useState(null);
  const [err, setErr] = useState(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [reveal, setReveal] = useState(null); // { name, fingerprint, private_key_hex }

  const load = async () => {
    setErr(null);
    try { setItems(await admin.listRootKeys() || []); } catch (e) { setErr(e); }
  };
  useEffect(() => { load(); }, []);

  const activate = async (k) => {
    const ok = await confirm({
      title: 'Activate root key?',
      message: `New licenses will be signed by "${k.name}". The currently active key will be deactivated.`,
      confirmLabel: 'Activate key',
      danger: false,
    });
    if (!ok) return;
    try { await admin.activateRootKey(k.id); toast.success(`Activated "${k.name}"`); await load(); }
    catch (e) { toast.error(e.message); }
  };

  const remove = async (k) => {
    const ok = await confirm({
      title: 'Delete root key?',
      message: `"${k.name}" will be deleted. Any .lic files previously signed by it will stop verifying.`,
      confirmLabel: 'Delete key',
      danger: true,
    });
    if (!ok) return;
    try { await admin.deleteRootKey(k.id); toast.success(`Deleted "${k.name}"`); await load(); }
    catch (e) { toast.error(e.message); }
  };

  const columns = [
    { key: 'name', header: 'Name', render: (k) => <span className="font-medium">{k.name}</span> },
    {
      key: 'role',
      header: 'Role',
      render: (k) => k.has_private_key
        ? <Badge color="blue">signing</Badge>
        : <Badge color="gray">verify-only</Badge>,
    },
    {
      key: 'active',
      header: 'Active',
      render: (k) => k.active
        ? <Badge color="green">active</Badge>
        : <span className="text-xs text-g-text-disabled">—</span>,
    },
    { key: 'fp', header: 'Fingerprint', render: (k) => <span className="font-mono text-xs">{k.fingerprint}</span> },
    { key: 'imported_from', header: 'Source', render: (k) => <span className="text-xs text-g-text-secondary">{k.imported_from || '—'}</span> },
    { key: 'created_at', header: 'Created', render: (k) => <span className="text-xs">{k.created_at ? new Date(k.created_at).toLocaleString() : '—'}</span> },
    {
      key: 'actions',
      header: '',
      className: 'text-right',
      render: (k) => (
        <div className="flex gap-1 justify-end">
          {!k.active && k.has_private_key && (
            <IconButton icon={<MdRadioButtonChecked />} label="Activate" onClick={() => activate(k)} />
          )}
          <IconButton
            icon={<MdDelete />}
            label={k.active ? 'Active key cannot be deleted' : 'Delete'}
            variant="danger"
            disabled={k.active}
            onClick={() => remove(k)}
          />
        </div>
      ),
    },
  ];

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-sm text-g-text-secondary max-w-xl">
          Ed25519 signing keys for license issuance. Private keys are KEK-encrypted at rest.
        </p>
        <Button variant="primary" icon={<MdAdd />} onClick={() => setCreateOpen(true)}>New root key</Button>
      </div>

      <ErrorBanner error={err} />

      {items === null ? <Spinner label="Loading root keys" /> : items.length === 0 ? (
        <EmptyState
          icon={MdKey}
          title="No root keys yet"
          description="Generate a fresh keypair (server-side) or upload an existing cnaklic private key to start issuing licenses."
          action={<Button variant="primary" icon={<MdAdd />} onClick={() => setCreateOpen(true)}>New root key</Button>}
        />
      ) : (
        <Table columns={columns} rows={items} />
      )}

      <CreateModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={(created) => {
          setCreateOpen(false);
          load();
          if (created.private_key_hex) {
            setReveal({
              name: created.name,
              fingerprint: created.fingerprint,
              private_key_hex: created.private_key_hex,
            });
          } else {
            toast.success(`Uploaded "${created.name}"`);
          }
        }}
      />

      <RevealModal reveal={reveal} onClose={() => setReveal(null)} />
    </div>
  );
}

function CreateModal({ open, onClose, onCreated }) {
  const [form, setForm] = useState({ name: '', mode: 'generate', private_key_hex: '' });
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) { setForm({ name: '', mode: 'generate', private_key_hex: '' }); setErr(null); }
  }, [open]);

  const set = (k) => (e) => setForm((f) => ({ ...f, [k]: e.target.value }));

  const uploadInvalid =
    form.mode === 'upload' && form.private_key_hex && !hexLooksValid(form.private_key_hex);

  const save = async () => {
    setErr(null); setBusy(true);
    try {
      const body = { name: form.name.trim(), mode: form.mode };
      if (form.mode === 'upload') body.private_key_hex = cleanHex(form.private_key_hex);
      const created = await admin.createRootKey(body);
      onCreated(created);
    } catch (e) { setErr(e); }
    finally { setBusy(false); }
  };

  const canSave =
    !!form.name.trim() &&
    (form.mode === 'generate' || (form.mode === 'upload' && hexLooksValid(form.private_key_hex)));

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New root key"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!canSave} onClick={save}>
            {busy ? 'Saving…' : (form.mode === 'generate' ? 'Generate' : 'Upload')}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <Input
          label="Name *"
          value={form.name}
          onChange={set('name')}
          placeholder="prod-signing"
          hint="A short identifier visible only to admins."
        />
        <Select
          label="Mode"
          value={form.mode}
          onChange={set('mode')}
          options={MODE_OPTS}
        />
        {form.mode === 'upload' && (
          <Textarea
            label="Private key (hex) *"
            mono
            rows={4}
            value={form.private_key_hex}
            onChange={set('private_key_hex')}
            placeholder="128 hex characters (64 bytes Ed25519)"
            hint={
              uploadInvalid
                ? 'Expected 128 hex characters (64 bytes ed25519.PrivateKey)'
                : 'Encrypted at rest with the server KEK. Never returned via the API.'
            }
          />
        )}
        {form.mode === 'generate' && (
          <div className="text-xs text-g-text-secondary border border-g-border-weak rounded p-3 bg-g-canvas">
            The server will generate a fresh Ed25519 keypair, KEK-encrypt the private
            half, and return the private key hex <span className="font-semibold">once</span>
            in the response. Save it in a password manager — it will never be displayed again.
          </div>
        )}
        <ErrorBanner error={err} />
      </div>
    </Modal>
  );
}

// RevealModal shows the freshly-minted private key hex once. The user must
// tick "I have saved this key" to close, so a careless click can't lose it.
function RevealModal({ reveal, onClose }) {
  const [ack, setAck] = useState(false);
  useEffect(() => { setAck(false); }, [reveal]);
  if (!reveal) return null;

  return (
    <Modal
      open={!!reveal}
      onClose={ack ? onClose : undefined}
      title="Private key — shown once"
      size="lg"
      footer={
        <Button variant="primary" disabled={!ack} onClick={onClose} icon={<MdCheckCircle />}>
          {ack ? 'I have saved this key — close' : 'Acknowledge to enable close'}
        </Button>
      }
    >
      <div className="space-y-3">
        <div className="rounded border border-g-red-text/40 bg-g-red-text/5 px-3 py-2 text-sm text-g-red-text">
          This is the only time this private key will be displayed. Store it in a password
          manager or sealed secret store now. Closing this dialog discards the displayed copy.
        </div>
        <dl className="grid grid-cols-2 gap-3 text-sm">
          <div>
            <dt className="text-[11px] font-semibold uppercase tracking-wider text-g-text-secondary">Name</dt>
            <dd className="text-g-text mt-0.5">{reveal.name}</dd>
          </div>
          <div>
            <dt className="text-[11px] font-semibold uppercase tracking-wider text-g-text-secondary">Fingerprint</dt>
            <dd className="text-g-text mt-0.5 font-mono text-xs">{reveal.fingerprint}</dd>
          </div>
        </dl>
        <div>
          <div className="text-[11px] font-semibold uppercase tracking-wider text-g-text-secondary mb-1">
            Private key (hex)
          </div>
          <CopyableCode value={reveal.private_key_hex} language="hex" />
        </div>
        <label className="flex items-center gap-2 text-sm text-g-text select-none">
          <input
            type="checkbox"
            checked={ack}
            onChange={(e) => setAck(e.target.checked)}
            className="h-4 w-4"
          />
          I have saved this key in a password manager / secret store.
        </label>
      </div>
    </Modal>
  );
}
