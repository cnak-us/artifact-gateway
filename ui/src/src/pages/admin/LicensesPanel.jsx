import { useEffect, useState } from 'react';
import { MdAdd, MdAutoFixHigh, MdClose, MdDelete, MdVpnKey, MdVisibility } from 'react-icons/md';
import { admin } from '../../api/client.js';
import { useToast } from '../../components/Toast.jsx';
import { useConfirm } from '../../components/ConfirmDialog.jsx';
import Button from '../../components/Button.jsx';
import IconButton from '../../components/IconButton.jsx';
import Modal from '../../components/Modal.jsx';
import Drawer from '../../components/Drawer.jsx';
import Spinner from '../../components/Spinner.jsx';
import ErrorBanner from '../../components/ErrorBanner.jsx';
import Table from '../../components/Table.jsx';
import Badge from '../../components/Badge.jsx';
import EmptyState from '../../components/EmptyState.jsx';
import Input from '../../components/Input.jsx';
import Textarea from '../../components/Textarea.jsx';
import Select from '../../components/Select.jsx';
import CopyableCode from '../../components/CopyableCode.jsx';

// Accept either a raw array OR a wrapped {key: [...]} response shape so a
// future backend reshape can't crash the SPA.
function asArray(v, key) {
  if (Array.isArray(v)) return v;
  if (v && Array.isArray(v[key])) return v[key];
  return [];
}

export default function LicensesPanel() {
  const toast = useToast();
  const confirm = useConfirm();
  const [items, setItems] = useState(null);
  const [err, setErr] = useState(null);
  const [uploadOpen, setUploadOpen] = useState(false);
  const [generateOpen, setGenerateOpen] = useState(false);
  const [selectedId, setSelectedId] = useState(null);

  const load = async () => {
    setErr(null);
    try { setItems(await admin.listLicenses() || []); } catch (e) { setErr(e); }
  };
  useEffect(() => { load(); }, []);

  const revoke = async (lic) => {
    const ok = await confirm({
      title: 'Revoke license?',
      message: `License "${lic.license_id}" will be revoked. Customer tokens bound to it will be denied immediately.`,
      confirmLabel: 'Revoke license',
      danger: true,
    });
    if (!ok) return;
    try { await admin.revokeLicense(lic.id); toast.success('License revoked'); await load(); }
    catch (e) { toast.error(e.message); }
  };

  // viewAs mints an ag_customer_session bound to this license, then navigates
  // to /catalog. window.location.assign is used (not react-router) because the
  // catalog SPA is a separate auth context and needs a real page load to read
  // the new cookie.
  const viewAs = async (lic) => {
    try {
      await admin.viewAsCustomer(lic.license_id);
      window.location.assign('/catalog');
    } catch (e) {
      toast.error(e.message);
    }
  };

  const columns = [
    { key: 'license_id', header: 'License ID', render: (l) => <span className="font-mono text-xs">{l.license_id}</span> },
    { key: 'customer', header: 'Customer', render: (l) => l.customer || '—' },
    { key: 'organization', header: 'Org', render: (l) => l.organization || '—' },
    { key: 'tier', header: 'Tier', render: (l) => <Badge color="blue">{l.tier || '—'}</Badge> },
    { key: 'expires', header: 'Expires', render: (l) => <span className="text-xs">{l.expires_at ? new Date(l.expires_at).toLocaleDateString() : 'never'}</span> },
    {
      key: 'status',
      header: 'Status',
      render: (l) => l.revoked_at
        ? <Badge color="red">revoked</Badge>
        : <Badge color="green">active</Badge>,
    },
    {
      key: 'actions',
      header: '',
      className: 'text-right',
      render: (l) => !l.revoked_at ? (
        <div className="inline-flex items-center gap-1">
          <IconButton
            icon={<MdVisibility />}
            label="View as customer"
            variant="default"
            onClick={(e) => { e.stopPropagation?.(); viewAs(l); }}
          />
          <IconButton
            icon={<MdDelete />}
            label="Revoke"
            variant="danger"
            onClick={(e) => { e.stopPropagation?.(); revoke(l); }}
          />
        </div>
      ) : null,
    },
  ];

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-sm text-g-text-secondary max-w-xl">
          Customer entitlements. The .lic blob is re-verified on every token mint.
        </p>
        <div className="flex gap-2">
          <Button variant="outline" icon={<MdAdd />} onClick={() => setUploadOpen(true)}>Upload license</Button>
          <Button variant="primary" icon={<MdAutoFixHigh />} onClick={() => setGenerateOpen(true)}>Generate license</Button>
        </div>
      </div>

      <ErrorBanner error={err} />

      {items === null ? <Spinner label="Loading licenses" /> : items.length === 0 ? (
        <EmptyState
          icon={MdVpnKey}
          title="No licenses yet"
          description="Generate a new license signed by an active root key, or upload an existing .lic blob."
          action={
            <div className="flex gap-2 justify-center">
              <Button variant="outline" icon={<MdAdd />} onClick={() => setUploadOpen(true)}>Upload license</Button>
              <Button variant="primary" icon={<MdAutoFixHigh />} onClick={() => setGenerateOpen(true)}>Generate license</Button>
            </div>
          }
        />
      ) : (
        <Table columns={columns} rows={items} onRowClick={(l) => setSelectedId(l.id)} />
      )}

      <UploadModal
        open={uploadOpen}
        onClose={() => setUploadOpen(false)}
        onSaved={() => { setUploadOpen(false); toast.success('License uploaded'); load(); }}
      />

      <GenerateModal
        open={generateOpen}
        onClose={() => setGenerateOpen(false)}
        onIssued={() => { toast.success('License issued'); load(); }}
      />

      <LicenseDrawer
        id={selectedId}
        onClose={() => setSelectedId(null)}
        onChanged={load}
      />
    </div>
  );
}

function UploadModal({ open, onClose, onSaved }) {
  const [blob, setBlob] = useState('');
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => { if (!open) { setBlob(''); setErr(null); } }, [open]);

  const save = async () => {
    setErr(null); setBusy(true);
    try { await admin.uploadLicense(blob); onSaved(); }
    catch (e) { setErr(e); }
    finally { setBusy(false); }
  };
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Upload .lic"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!blob.trim()} onClick={save}>{busy ? 'Uploading…' : 'Upload'}</Button>
        </>
      }
    >
      <div className="space-y-2">
        <Textarea
          label="License blob"
          mono
          rows={10}
          value={blob}
          onChange={(e) => setBlob(e.target.value)}
          placeholder="paste the full .lic file here"
        />
        <ErrorBanner error={err} />
      </div>
    </Modal>
  );
}

function LicenseDrawer({ id, onClose, onChanged }) {
  const toast = useToast();
  const confirm = useConfirm();
  const [lic, setLic] = useState(null);
  const [grants, setGrants] = useState([]);
  const [packages, setPackages] = useState([]);
  const [contacts, setContacts] = useState([]);
  const [contactEmail, setContactEmail] = useState('');
  const [contactName, setContactName] = useState('');
  const [contactBusy, setContactBusy] = useState(false);
  const [err, setErr] = useState(null);
  // Set of package IDs queued in the "Add package grant" multi-picker. We use
  // a Set (not array) so toggling a checkbox is O(1) regardless of list size.
  const [selectedToAdd, setSelectedToAdd] = useState(() => new Set());
  const [grantBusy, setGrantBusy] = useState(false);

  useEffect(() => {
    if (!id) {
      setLic(null); setGrants([]); setContacts([]);
      // Also clear the contact-form inputs so reopening the drawer for
      // another license doesn't preserve a half-typed email/name from the
      // previous session.
      setContactEmail(''); setContactName(''); setSelectedToAdd(new Set());
      return;
    }
    setErr(null);
    Promise.all([
      admin.getLicense(id),
      admin.getLicenseGrants(id),
      admin.listPackages(),
      admin.listContacts(id),
    ]).then(([l, gs, ps, cs]) => {
      setLic(l);
      setGrants(asArray(gs, 'grants'));
      setPackages(asArray(ps, 'packages'));
      setContacts(asArray(cs, 'contacts'));
    }).catch(setErr);
  }, [id]);

  const grantedPkgIds = new Set(grants.map((g) => g.package_id));
  const available = packages.filter((p) => !grantedPkgIds.has(p.id));

  const updateGrants = async (next) => {
    try {
      await admin.putLicenseGrants(id, next.map((g) => ({ package_id: g.package_id, actions: g.actions || ['pull'] })));
      setGrants(next);
      onChanged?.();
    } catch (e) { toast.error(e.message); }
  };

  const removeGrant = (pkgId) => updateGrants(grants.filter((g) => g.package_id !== pkgId));

  const toggleSelected = (pkgId) => {
    setSelectedToAdd((prev) => {
      const next = new Set(prev);
      if (next.has(pkgId)) next.delete(pkgId);
      else next.add(pkgId);
      return next;
    });
  };
  const selectAllAvailable = () => setSelectedToAdd(new Set(available.map((p) => p.id)));
  const clearSelection = () => setSelectedToAdd(new Set());

  const addGrants = async () => {
    if (selectedToAdd.size === 0) return;
    setGrantBusy(true);
    try {
      const next = [
        ...grants,
        ...available
          .filter((p) => selectedToAdd.has(p.id))
          .map((p) => ({ package_id: p.id, actions: ['pull'] })),
      ];
      await updateGrants(next);
      setSelectedToAdd(new Set());
    } finally {
      setGrantBusy(false);
    }
  };

  // Mirror the backend regex so we surface bad input as a toast before the
  // POST. Backend stays canonical (lowercase + trim) — we don't pre-lower
  // here, just trim, so the operator sees what they typed.
  const emailLooksValid = (s) => /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(s);

  const addContact = async () => {
    const email = contactEmail.trim();
    const name = contactName.trim();
    if (!email) { toast.error('Email is required'); return; }
    if (!emailLooksValid(email)) { toast.error('That email address looks malformed'); return; }
    setContactBusy(true);
    try {
      const created = await admin.addContact(id, email, name);
      // Server canonicalizes; merge using the server-returned email so we
      // don't double-add on case-only differences.
      setContacts((prev) => {
        const next = prev.filter((c) => c.email !== created.email);
        next.push(created);
        return next;
      });
      setContactEmail('');
      setContactName('');
      toast.success('Contact added');
      onChanged?.();
    } catch (e) { toast.error(e.message); }
    finally { setContactBusy(false); }
  };

  const removeContact = async (email) => {
    const ok = await confirm({
      title: 'Remove contact?',
      message: `${email} will be removed from this license and will lose SSO access to its entitlements.`,
      confirmLabel: 'Remove contact',
      danger: true,
    });
    if (!ok) return;
    try {
      await admin.removeContact(id, email);
      setContacts((prev) => prev.filter((c) => c.email !== email));
      toast.success('Contact removed');
      onChanged?.();
    } catch (e) { toast.error(e.message); }
  };

  return (
    <Drawer open={!!id} onClose={onClose} title={lic ? `License ${lic.license_id}` : 'License'}>
      <ErrorBanner error={err} />
      {!lic ? <Spinner label="Loading" /> : (
        <div className="space-y-5">
          <dl className="grid grid-cols-2 gap-3 text-sm">
            <Cell label="Customer" value={lic.customer || '—'} />
            <Cell label="Organization" value={lic.organization || '—'} />
            <Cell label="Tier"><Badge color="blue">{lic.tier || '—'}</Badge></Cell>
            <Cell label="Expires" value={lic.expires_at ? new Date(lic.expires_at).toLocaleString() : 'never'} />
            <Cell label="Status">
              {lic.revoked_at ? <Badge color="red">revoked</Badge> : <Badge color="green">active</Badge>}
            </Cell>
            <Cell label="Uploaded" value={lic.created_at ? new Date(lic.created_at).toLocaleString() : '—'} />
          </dl>

          <div>
            <h4 className="text-xs font-semibold uppercase tracking-wider text-g-text-secondary mb-2">Granted packages</h4>
            <div className="flex flex-wrap gap-1">
              {grants.length === 0 && (
                <div className="text-sm text-g-text-secondary">No grants yet — this license has no entitled packages.</div>
              )}
              {grants.map((g) => {
                const p = packages.find((x) => x.id === g.package_id);
                return (
                  <Badge key={g.package_id} color="gray">
                    {p ? (p.display_name || p.slug) : g.package_id}
                    <button
                      onClick={() => removeGrant(g.package_id)}
                      className="ml-1 text-g-text-secondary hover:text-g-red-text"
                      title="Remove"
                    >
                      <MdClose className="text-xs" />
                    </button>
                  </Badge>
                );
              })}
            </div>
          </div>

          <div>
            <div className="flex items-baseline justify-between gap-3 mb-2">
              <h4 className="text-xs font-semibold uppercase tracking-wider text-g-text-secondary">Add package grant</h4>
              {available.length > 0 && (
                <div className="flex items-center gap-3 text-xs">
                  <button
                    type="button"
                    onClick={selectedToAdd.size === available.length ? clearSelection : selectAllAvailable}
                    className="text-g-accent-text hover:underline"
                  >
                    {selectedToAdd.size === available.length ? 'Clear' : `Select all (${available.length})`}
                  </button>
                </div>
              )}
            </div>
            {available.length === 0 ? (
              <div className="text-sm text-g-text-secondary">All packages are already granted.</div>
            ) : (
              <>
                <div className="max-h-64 overflow-y-auto border border-g-border-weak rounded divide-y divide-g-border-weak">
                  {available.map((p) => {
                    const checked = selectedToAdd.has(p.id);
                    return (
                      <label
                        key={p.id}
                        className="flex items-center gap-2 px-3 py-2 text-sm cursor-pointer hover:bg-g-hover"
                      >
                        <input
                          type="checkbox"
                          checked={checked}
                          onChange={() => toggleSelected(p.id)}
                          className="rounded border-g-border-medium bg-g-secondary text-g-accent-main focus:ring-g-accent-main/40"
                        />
                        <span className="flex-1 truncate">{p.display_name || p.slug}</span>
                        {p.display_name && (
                          <span className="text-xs text-g-text-secondary font-mono truncate">{p.slug}</span>
                        )}
                      </label>
                    );
                  })}
                </div>
                <div className="mt-2 flex justify-end">
                  <Button
                    variant="primary"
                    onClick={addGrants}
                    disabled={selectedToAdd.size === 0 || grantBusy}
                  >
                    {grantBusy
                      ? 'Granting…'
                      : selectedToAdd.size <= 1
                        ? 'Grant'
                        : `Grant ${selectedToAdd.size} packages`}
                  </Button>
                </div>
              </>
            )}
          </div>

          <div>
            <h4 className="text-xs font-semibold uppercase tracking-wider text-g-text-secondary mb-1">Named contacts</h4>
            <p className="text-xs text-g-text-secondary mb-2">
              Email addresses allowed to sign into the catalog via SSO and inherit this license's entitlements.
            </p>
            {contacts.length === 0 ? (
              <div className="text-sm text-g-text-secondary">
                No contacts yet — add one to allow SSO sign-in for this license.
              </div>
            ) : (
              <ul className="divide-y divide-g-border rounded border border-g-border">
                {contacts.map((c) => (
                  <li key={c.email} className="flex items-center gap-2 px-2 py-1.5">
                    <div className="flex-1 min-w-0">
                      <div className="text-sm font-mono truncate">{c.email}</div>
                      {c.name && <div className="text-xs text-g-text-secondary truncate">{c.name}</div>}
                    </div>
                    <IconButton
                      icon={<MdDelete />}
                      label="Remove"
                      variant="danger"
                      onClick={() => removeContact(c.email)}
                    />
                  </li>
                ))}
              </ul>
            )}
          </div>

          <div>
            <h4 className="text-xs font-semibold uppercase tracking-wider text-g-text-secondary mb-2">Add contact</h4>
            <div className="flex gap-2">
              <Input
                type="email"
                value={contactEmail}
                onChange={(e) => setContactEmail(e.target.value)}
                placeholder="email (required)"
                aria-label="Email"
                className="flex-1"
              />
              <Input
                value={contactName}
                onChange={(e) => setContactName(e.target.value)}
                placeholder="name (optional)"
                aria-label="Name"
                className="flex-1"
              />
              <Button variant="primary" onClick={addContact} loading={contactBusy} disabled={!contactEmail.trim()}>Add</Button>
            </div>
          </div>
        </div>
      )}
    </Drawer>
  );
}

const TIER_OPTS = [
  { value: 'trial',        label: 'Trial' },
  { value: 'professional', label: 'Professional' },
  { value: 'enterprise',   label: 'Enterprise' },
];

const EXPIRY_OPTS = [
  { value: 'never',   label: 'Never (perpetual)' },
  { value: '30d',     label: '30 days' },
  { value: '90d',     label: '90 days' },
  { value: '365d',    label: '1 year' },
  { value: '2y',      label: '2 years' },
  { value: 'custom',  label: 'Custom date…' },
];

// Preset attribute keys CNAK consumes today. Free-text keys are still
// accepted in case an operator wants to encode something experimental; the
// list just powers the <datalist> suggestion in the editor and the type
// hint shown next to the value input.
const ATTRIBUTE_PRESETS = [
  { key: 'max_tracks',           hint: 'integer', placeholder: '50000',
    description: 'Hard cap on concurrent tracks in the live store.' },
  { key: 'max_users',            hint: 'integer', placeholder: '25',
    description: 'Maximum total user accounts.' },
  { key: 'max_federation_peers', hint: 'integer', placeholder: '10',
    description: 'Maximum number of federation peers.' },
  { key: 'max_groups',           hint: 'integer', placeholder: '50',
    description: 'Maximum number of CoT groups.' },
  { key: 'max_classification',   hint: 'enum',    placeholder: 'secret',
    description: 'Highest classification level allowed (unclassified | cui | confidential | secret | topsecret).' },
  { key: 'plugins_enabled',      hint: 'boolean', placeholder: 'true',
    description: 'Whether the customer is licensed to install plugins (true/false).' },
];

// Default attribute rows pre-populated when the dialog opens. Keep parity
// with the previous behaviour (max_tracks = 50000 visible) so the most
// common knob is one keystroke away rather than buried behind a button.
function defaultAttributes() {
  return [{ key: 'max_tracks', value: '50000' }];
}

function GenerateModal({ open, onClose, onIssued }) {
  const blank = {
    customer: '', organization: '', poc_name: '', poc_email: '',
    tier: 'professional',
    expiry_choice: '365d', expires_at: '',
    root_key_id: '',
  };
  const [form, setForm] = useState(blank);
  // Attributes are kept as an ordered list (not a map) so two operators
  // can't both add "max_tracks" and have the second silently overwrite the
  // first — the editor flags the duplicate instead, then we collapse to a
  // map at submit time.
  const [attrs, setAttrs] = useState(defaultAttributes);
  const [keys, setKeys] = useState(null);  // null = loading
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);
  const [issued, setIssued] = useState(null); // {license, lic_blob}

  useEffect(() => {
    if (!open) {
      setForm(blank); setAttrs(defaultAttributes()); setErr(null); setIssued(null);
      return;
    }
    admin.listRootKeys()
      .then((rows) => {
        const signing = (rows || []).filter((k) => k.has_private_key);
        setKeys(signing);
        const active = signing.find((k) => k.active);
        if (active) setForm((f) => ({ ...f, root_key_id: active.id }));
      })
      .catch(setErr);
  }, [open]);

  const set = (k) => (e) => setForm((f) => ({ ...f, [k]: e.target.value }));

  // Switching expiry_choice away from 'custom' hides the expires_at input;
  // clear its stored value too so a stale RFC3339 string doesn't pop back
  // into view if the operator toggles back to 'custom'.
  const setExpiryChoice = (e) => {
    const next = e.target.value;
    setForm((f) => ({ ...f, expiry_choice: next, expires_at: next === 'custom' ? f.expires_at : '' }));
  };

  // Attribute editor helpers — kept tiny since the underlying state is a
  // plain ordered list of {key, value} entries.
  const addAttr   = ()      => setAttrs((rows) => [...rows, { key: '', value: '' }]);
  const removeAttr = (i)    => setAttrs((rows) => rows.filter((_, idx) => idx !== i));
  const updateAttr = (i, field) => (e) =>
    setAttrs((rows) => rows.map((r, idx) => (idx === i ? { ...r, [field]: e.target.value } : r)));

  // Collapse the editor's ordered list into the wire-format attributes map.
  // Blank rows and rows with duplicate keys (later wins) are dropped here so
  // the server never has to deal with malformed input.
  const collectAttributes = () => {
    const out = {};
    for (const row of attrs) {
      const k = (row.key || '').trim();
      const v = (row.value || '').trim();
      if (!k || !v) continue;
      out[k] = v;
    }
    return out;
  };

  // Surface duplicate-key conflicts in the UI so the operator can fix them
  // before submit instead of being surprised by a silent server-side merge.
  const duplicateKeys = (() => {
    const seen = new Set();
    const dupes = new Set();
    for (const row of attrs) {
      const k = (row.key || '').trim();
      if (!k) continue;
      if (seen.has(k)) dupes.add(k);
      seen.add(k);
    }
    return dupes;
  })();

  const submit = async () => {
    setErr(null); setBusy(true);
    try {
      const body = {
        customer: form.customer.trim(),
        organization: form.organization.trim(),
        poc_name: form.poc_name.trim(),
        poc_email: form.poc_email.trim(),
        tier: form.tier,
        attributes: collectAttributes(),
      };
      if (form.expiry_choice === 'custom') {
        body.expires_at = form.expires_at;
      } else {
        body.duration = form.expiry_choice;
      }
      if (form.root_key_id) body.root_key_id = form.root_key_id;
      const res = await admin.issueLicense(body);
      setIssued(res);
      onIssued?.();
    } catch (e) { setErr(e); }
    finally { setBusy(false); }
  };

  const downloadLicBlob = () => {
    if (!issued?.lic_blob) return;
    const safe = (issued.license.customer || 'license').toLowerCase().replace(/[^a-z0-9]+/g, '-');
    const blob = new Blob([issued.lic_blob], { type: 'text/plain' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${safe}-${issued.license.license_id}.lic`;
    document.body.appendChild(a); a.click(); a.remove();
    URL.revokeObjectURL(url);
  };

  const canSubmit =
    form.customer.trim() &&
    form.tier &&
    duplicateKeys.size === 0 &&
    (form.expiry_choice !== 'custom' || !!form.expires_at);

  if (issued) {
    return (
      <Modal
        open={open}
        onClose={onClose}
        title="License issued"
        size="lg"
        footer={
          <>
            <Button variant="ghost" onClick={onClose}>Close</Button>
            <Button variant="primary" onClick={downloadLicBlob}>Download .lic</Button>
          </>
        }
      >
        <div className="space-y-3">
          <div className="text-sm text-g-text-secondary">
            License <span className="font-mono">{issued.license.license_id}</span> issued for
            <span className="font-medium text-g-text"> {issued.license.customer}</span>.
            Copy the blob below or click <em>Download .lic</em> to save it.
          </div>
          <CopyableCode value={issued.lic_blob} language="lic" />
        </div>
      </Modal>
    );
  }

  const noSigningKey = keys !== null && keys.length === 0;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Generate license"
      size="lg"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!canSubmit || noSigningKey} onClick={submit}>
            {busy ? 'Signing…' : 'Generate'}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {noSigningKey && (
          <div className="rounded border border-g-red-text/40 bg-g-red-text/5 px-3 py-2 text-sm text-g-red-text">
            No signing key available. Add one under the <span className="font-semibold">Root keys</span> tab first.
          </div>
        )}

        <div className="grid grid-cols-2 gap-3">
          <Input label="Customer *" value={form.customer} onChange={set('customer')} placeholder="Acme Corp" />
          <Input label="Organization" value={form.organization} onChange={set('organization')} placeholder="Acme Inc" />
          <Input label="POC name" value={form.poc_name} onChange={set('poc_name')} placeholder="Jane Doe" />
          <Input label="POC email" value={form.poc_email} onChange={set('poc_email')} placeholder="jane@acme.example" />
          <Select label="Tier *" value={form.tier} onChange={set('tier')} options={TIER_OPTS} />
          <Select label="Expiry" value={form.expiry_choice} onChange={setExpiryChoice} options={EXPIRY_OPTS} />
          {form.expiry_choice === 'custom' && (
            <Input label="Expires at (RFC3339)" value={form.expires_at} onChange={set('expires_at')} placeholder="2027-01-01T00:00:00Z" />
          )}
        </div>

        <AttributeEditor
          rows={attrs}
          duplicateKeys={duplicateKeys}
          onAdd={addAttr}
          onRemove={removeAttr}
          onUpdate={updateAttr}
        />

        <div>
          <label className="block text-[11px] font-semibold uppercase tracking-wider text-g-text-secondary mb-1">
            Signing key
          </label>
          {keys === null ? <Spinner label="Loading keys" /> : (
            <Select
              value={form.root_key_id}
              onChange={set('root_key_id')}
              placeholder={noSigningKey ? '— no signing keys available —' : '— use the active signing key —'}
              options={keys.map((k) => ({
                value: k.id,
                label: `${k.name} · ${k.fingerprint}${k.active ? ' · active' : ''}`,
              }))}
            />
          )}
        </div>

        <ErrorBanner error={err} />
      </div>
    </Modal>
  );
}

function Cell({ label, value, children }) {
  return (
    <div>
      <dt className="text-[11px] font-semibold uppercase tracking-wider text-g-text-secondary">{label}</dt>
      <dd className="text-g-text mt-0.5">{children ?? value}</dd>
    </div>
  );
}

// AttributeEditor — ordered list of {key,value} rows submitted as a map.
// Each key input is backed by a <datalist> built from ATTRIBUTE_PRESETS so
// admins can autocomplete known CNAK knobs without losing the ability to
// type ad-hoc keys. The value column shows the preset's type hint when the
// current key matches a preset, giving operators a nudge toward the right
// format (integer / boolean / enum string).
function AttributeEditor({ rows, duplicateKeys, onAdd, onRemove, onUpdate }) {
  const listId = 'license-attr-presets';
  return (
    <div>
      <div className="flex items-center justify-between mb-1">
        <label className="text-[11px] font-semibold uppercase tracking-wider text-g-text-secondary">
          Attributes
        </label>
        <Button size="sm" variant="ghost" icon={<MdAdd />} onClick={onAdd}>
          Add attribute
        </Button>
      </div>
      <p className="text-xs text-g-text-secondary mb-2">
        Per-customer knobs the issued license carries (e.g. <code>max_tracks</code>).
        Stored verbatim in the signed payload — CNAK reads them, enforcement is policy-dependent.
      </p>

      <datalist id={listId}>
        {ATTRIBUTE_PRESETS.map((p) => (
          <option key={p.key} value={p.key}>{p.description}</option>
        ))}
      </datalist>

      <div className="space-y-2">
        {rows.length === 0 && (
          <div className="text-xs text-g-text-disabled italic px-2 py-1.5 border border-dashed border-g-border-weak rounded">
            No attributes set. The issued license will carry no per-customer knobs.
          </div>
        )}
        {rows.map((row, i) => {
          const trimmed = (row.key || '').trim();
          const preset = ATTRIBUTE_PRESETS.find((p) => p.key === trimmed);
          const isDuplicate = trimmed && duplicateKeys.has(trimmed);
          return (
            <div key={i} className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] gap-2 items-start">
              <Input
                list={listId}
                value={row.key}
                onChange={onUpdate(i, 'key')}
                placeholder="key (e.g. max_tracks)"
                error={isDuplicate ? 'Duplicate key' : undefined}
              />
              <Input
                value={row.value}
                onChange={onUpdate(i, 'value')}
                placeholder={preset?.placeholder || 'value'}
                hint={preset ? `${preset.hint} — ${preset.description}` : undefined}
              />
              <IconButton
                icon={<MdDelete />}
                label="Remove attribute"
                onClick={() => onRemove(i)}
              />
            </div>
          );
        })}
      </div>
    </div>
  );
}
