import { useEffect, useState } from 'react';
import { MdAdd, MdDelete, MdLockOutline, MdNetworkCheck } from 'react-icons/md';
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
import Textarea from '../../components/Textarea.jsx';
import Select from '../../components/Select.jsx';
import ProbeResultModal from '../../components/ProbeResultModal.jsx';

function asArray(v, key) {
  if (Array.isArray(v)) return v;
  if (v && Array.isArray(v[key])) return v[key];
  return [];
}

const KIND_OPTS = [
  { value: 'ghcr',       label: 'GHCR (registry)' },
  { value: 'github-api', label: 'GitHub API (releases)' },
  { value: 'gitlab-api', label: 'GitLab API (releases)' },
  { value: 'oci-basic',  label: 'OCI registry — Basic auth (Gitea / Harbor / Artifactory / ACR scope-mapped)' },
  { value: 'dockerhub',  label: 'Docker Hub' },
  { value: 'quay',       label: 'Quay.io' },
  { value: 'gitlab',     label: 'GitLab Container Registry' },
  { value: 'ecr',        label: 'AWS ECR (cloud issuer)' },
  { value: 'gar',        label: 'Google Artifact Registry / GCR (cloud issuer)' },
  { value: 'acr-aad',    label: 'Azure Container Registry — AAD (cloud issuer)' },
];

const KIND_BADGE = {
  'ghcr':       { color: 'gray',   label: 'ghcr' },
  'github-api': { color: 'purple', label: 'github-api' },
  'gitlab-api': { color: 'purple', label: 'gitlab-api' },
  'oci-basic':  { color: 'blue',   label: 'oci-basic' },
  'dockerhub':  { color: 'blue',   label: 'dockerhub' },
  'quay':       { color: 'red',    label: 'quay' },
  'gitlab':     { color: 'orange', label: 'gitlab' },
  'ecr':        { color: 'yellow', label: 'ecr' },
  'gar':        { color: 'yellow', label: 'gar' },
  'acr-aad':    { color: 'yellow', label: 'acr-aad' },
};

// stripScheme renders a host as a compact "host[:port]/path" string for the
// table — drops https:// noise that all rows share.
function stripScheme(u) {
  if (!u) return '';
  return u.replace(/^https?:\/\//, '');
}

// KIND_HINTS is the per-Kind copy shown under the Kind selector. Keep these in
// sync with the README "Upstream credentials" table and the server-side
// validUpstreamCredKinds allowlist.
const KIND_HINTS = {
  'ghcr':       "Classic PAT with 'read:packages'. GHCR does not support fine-grained PATs for docker pull.",
  'github-api': "Classic PAT with 'repo' (private) or 'public_repo' (public). Fine-grained PAT: Contents=Read, Metadata=Read on selected repos.",
  'gitlab-api': "PAT or Project/Group Access Token with 'read_api' (and 'read_repository' if the package is in a private project). Base URL is the GitLab host (e.g. https://gitlab.com or your self-hosted host).",
  'oci-basic':  "Token with pull/read on the target repository. Gitea: 'read:package'. Harbor: robot account with pull+read. Artifactory: Identity Token with repo read. ACR scope-mapped tokens also fit here.",
  'dockerhub':  "Docker Hub PAT with Read scope. Username is your Docker ID. Host is pinned to registry-1.docker.io.",
  'quay':       "Robot account name (e.g. 'org+robot') and robot token with Read on the target repos. Defaults to quay.io; override Base URL for self-hosted Quay.",
  'gitlab':     "Project/Group Deploy Token (recommended) or PAT with 'read_registry'. Base URL is the registry host, e.g. registry.gitlab.com or registry.<self-hosted>.",
  'ecr':        "IAM credentials (accessKeyId/secretAccessKey JSON) with ecr:GetAuthorizationToken + ecr:BatchGetImage + ecr:GetDownloadUrlForLayer. The proxy mints a 12h Basic token and refreshes before expiry.",
  'gar':        "Google service-account JSON key with roles/artifactregistry.reader. The proxy mints an OAuth2 access token (~1h) and refreshes before expiry.",
  'acr-aad':    "Service principal (clientId/clientSecret JSON) with AcrPull on the registry. The proxy does AAD → ACR refresh → ACR access exchange and refreshes before expiry.",
};

// Per-Kind placeholders for the Issuer secret textarea (bucket-C only).
const ISSUER_SECRET_PLACEHOLDERS = {
  'ecr':     '{"accessKeyId":"AKIA…","secretAccessKey":"…"}',
  'gar':     '{ raw service-account JSON key here }',
  'acr-aad': '{"clientId":"…","clientSecret":"…"}',
};

const ISSUER_CONFIG_PLACEHOLDERS = {
  'ecr':     '{"region":"us-east-1","accountId":"123456789012"}',
  'gar':     '{}',
  'acr-aad': '{"tenantId":"…","registry":"myreg.azurecr.io"}',
};

export default function UpstreamCredentials() {
  const toast = useToast();
  const confirm = useConfirm();
  const [items, setItems] = useState(null);
  const [err, setErr] = useState(null);
  const [open, setOpen] = useState(false);
  const [probe, setProbe] = useState({ open: false, title: '', loading: false, result: null, error: null });

  const load = async () => {
    setErr(null);
    try {
      const res = await admin.listUpstreamCredentials();
      setItems(asArray(res, 'credentials'));
    } catch (e) { setErr(e); }
  };
  useEffect(() => { load(); }, []);

  const remove = async (c) => {
    const ok = await confirm({
      title: 'Delete credential?',
      message: `"${c.name}" will be deleted. Packages using it will break until reconfigured.`,
      confirmLabel: 'Delete credential',
      danger: true,
    });
    if (!ok) return;
    try { await admin.deleteUpstreamCredential(c.id); toast.success(`Deleted "${c.name}"`); await load(); }
    catch (e) { toast.error(e.message); }
  };

  const onTest = async (c) => {
    setProbe({ open: true, title: `Test "${c.name}" credential`, loading: true, result: null, error: null });
    try {
      const res = await admin.testUpstreamCredential(c.id);
      setProbe((p) => ({ ...p, loading: false, result: res, error: null }));
    } catch (e) {
      setProbe((p) => ({ ...p, loading: false, result: null, error: e }));
    }
  };

  const columns = [
    { key: 'name', header: 'Name', render: (c) => <span className="font-medium">{c.name}</span> },
    {
      key: 'kind',
      header: 'Kind',
      render: (c) => {
        const cfg = KIND_BADGE[c.kind] || { color: 'gray', label: c.kind || '—' };
        return (
          <div className="flex items-center gap-1.5">
            <Badge color={cfg.color}>{cfg.label}</Badge>
            {c.has_ca_bundle && <Badge color="gray">CA</Badge>}
            {c.insecure_skip_tls_verify && <Badge color="red">insecure</Badge>}
          </div>
        );
      },
    },
    {
      key: 'endpoint',
      header: 'Endpoint',
      render: (c) => (
        <span className="font-mono text-xs text-g-text-secondary">{stripScheme(c.endpoint) || '—'}</span>
      ),
    },
    {
      key: 'credential',
      header: 'Credential',
      render: (c) => {
        // Issuer-mint kinds carry no PAT — surface the cloud + a compact
        // config preview so the row isn't all dashes.
        if (c.issuer_kind) {
          return (
            <div className="flex flex-col gap-0.5 text-xs">
              <span className="text-g-text">issuer · {c.issuer_kind}</span>
              {c.issuer_config && (
                <span className="font-mono text-[10px] text-g-text-secondary truncate max-w-[28ch]" title={c.issuer_config}>
                  {c.issuer_config}
                </span>
              )}
            </div>
          );
        }
        return (
          <div className="flex flex-col gap-0.5 text-xs">
            <span>{c.username || '—'}</span>
            <span className="font-mono text-[10px] text-g-text-secondary">{c.pat_fingerprint || '—'}</span>
          </div>
        );
      },
    },
    { key: 'last_used', header: 'Last used', render: (c) => <span className="text-xs text-g-text-secondary">{c.last_used_at ? new Date(c.last_used_at).toLocaleString() : '—'}</span> },
    {
      key: 'actions',
      header: '',
      className: 'text-right',
      render: (c) => (
        <div className="flex gap-1 justify-end">
          <IconButton icon={<MdNetworkCheck />} label="Test" onClick={() => onTest(c)} />
          <IconButton icon={<MdDelete />} label="Delete" variant="danger" onClick={() => remove(c)} />
        </div>
      ),
    },
  ];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Upstream credentials</h1>
          <p className="text-sm text-g-text-secondary">Server-side credentials used to pull from upstream registries. Secrets are encrypted with the KEK and never returned.</p>
        </div>
        <Button variant="primary" icon={<MdAdd />} onClick={() => setOpen(true)}>New credential</Button>
      </div>

      <ErrorBanner error={err} />

      {items === null ? <Spinner label="Loading credentials" /> : items.length === 0 ? (
        <EmptyState
          icon={MdLockOutline}
          title="No upstream credentials"
          description="Add credentials for any supported registry (GHCR, GitHub releases, or any OCI registry with Basic auth such as Gitea, Harbor, or Artifactory) so packages can be proxied. Secrets are encrypted with the server KEK and never returned."
          action={<Button variant="primary" icon={<MdAdd />} onClick={() => setOpen(true)}>New credential</Button>}
        />
      ) : (
        <Table columns={columns} rows={items} />
      )}

      <CreateModal
        open={open}
        onClose={() => setOpen(false)}
        onSaved={(msg) => { setOpen(false); toast.success(msg); load(); }}
      />

      <ProbeResultModal
        open={probe.open}
        onClose={() => setProbe((p) => ({ ...p, open: false }))}
        title={probe.title}
        loading={probe.loading}
        result={probe.result}
        error={probe.error}
      />
    </div>
  );
}

const EMPTY_FORM = {
  name: '', kind: 'ghcr', username: '', pat: '',
  base_url: '', ca_bundle_pem: '', insecure_skip_tls_verify: false,
  issuer_secret: '', issuer_config: '',
};

const ISSUER_KINDS = new Set(['ecr', 'gar', 'acr-aad']);

function CreateModal({ open, onClose, onSaved }) {
  const [form, setForm] = useState(EMPTY_FORM);
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) { setForm(EMPTY_FORM); setErr(null); }
  }, [open]);

  const set = (k) => (e) => setForm((f) => ({ ...f, [k]: e.target.value }));
  const setBool = (k) => (e) => setForm((f) => ({ ...f, [k]: e.target.checked }));

  const isIssuer = ISSUER_KINDS.has(form.kind);
  const needsBaseURL = form.kind === 'oci-basic' || form.kind === 'gitlab' || form.kind === 'gitlab-api' || form.kind === 'gar' || form.kind === 'acr-aad';
  const allowsBaseURL = needsBaseURL || form.kind === 'quay';
  const showTLSFields = needsBaseURL || form.kind === 'quay';

  let disableSave = !form.name;
  if (isIssuer) disableSave = disableSave || !form.issuer_secret || (needsBaseURL && !form.base_url);
  else disableSave = disableSave || !form.pat || (needsBaseURL && !form.base_url);

  const save = async () => {
    setErr(null); setBusy(true);
    try {
      const body = {
        name: form.name, kind: form.kind,
        username: form.username || '', pat: form.pat || '',
        base_url: form.base_url || '',
        ca_bundle_pem: form.ca_bundle_pem || '',
        insecure_skip_tls_verify: form.insecure_skip_tls_verify,
      };
      if (isIssuer) {
        try { body.issuer_secret = JSON.parse(form.issuer_secret); }
        catch { throw new Error('issuer_secret must be valid JSON'); }
        if (form.issuer_config) {
          try { body.issuer_config = JSON.parse(form.issuer_config); }
          catch { throw new Error('issuer_config must be valid JSON'); }
        }
      }
      await admin.createUpstreamCredential(body);
      onSaved(`Created "${form.name}"`);
    } catch (e) { setErr(e); }
    finally { setBusy(false); }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New upstream credential"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={disableSave} onClick={save}>{busy ? 'Saving…' : 'Save'}</Button>
        </>
      }
    >
      <div className="space-y-3">
        <Input label="Name *" value={form.name} onChange={set('name')} placeholder="ghcr-prod" />
        <Select
          label="Kind"
          value={form.kind}
          onChange={set('kind')}
          options={KIND_OPTS}
          hint={KIND_HINTS[form.kind] || ''}
        />
        {allowsBaseURL && (
          <Input
            label={needsBaseURL ? 'Base URL *' : 'Base URL'}
            value={form.base_url}
            onChange={set('base_url')}
            placeholder={form.kind === 'quay' ? 'https://quay.io (default)' : 'https://gitea.example.com'}
            hint={form.kind === 'quay'
              ? 'Optional. Leave blank for quay.io; set for self-hosted Quay.'
              : 'Scheme + host (no trailing /v2). Required for this kind.'}
          />
        )}
        {!isIssuer && (
          <>
            <Input label="Username" value={form.username} onChange={set('username')} placeholder="cnak-bot" />
            <Input
              label="Personal access token *"
              type="password"
              value={form.pat}
              onChange={set('pat')}
              placeholder="ghp_..."
              hint="Stored AES-GCM encrypted with the server KEK. Never returned via the API."
              className="font-mono"
            />
          </>
        )}
        {isIssuer && (
          <>
            <Textarea
              label="Issuer secret (JSON) *"
              value={form.issuer_secret}
              onChange={set('issuer_secret')}
              placeholder={ISSUER_SECRET_PLACEHOLDERS[form.kind] || '{}'}
              rows={4}
              mono
              hint="Stored AES-GCM encrypted with the KEK. Never returned via the API."
            />
            <Textarea
              label="Issuer config (JSON, optional)"
              value={form.issuer_config}
              onChange={set('issuer_config')}
              placeholder={ISSUER_CONFIG_PLACEHOLDERS[form.kind] || '{}'}
              rows={3}
              mono
              hint="Non-secret per-cloud config (region, tenant, etc.). Stored as JSONB."
            />
          </>
        )}
        {showTLSFields && (
          <>
            <Textarea
              label="CA bundle (PEM)"
              value={form.ca_bundle_pem}
              onChange={set('ca_bundle_pem')}
              placeholder={'-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----'}
              rows={5}
              mono
              hint="Optional. Paste a PEM chain if the upstream uses an internal CA. Stored in plaintext (not a secret)."
            />
            <label className="flex items-center gap-2 text-sm text-g-text">
              <input
                type="checkbox"
                checked={form.insecure_skip_tls_verify}
                onChange={setBool('insecure_skip_tls_verify')}
                className="rounded border-g-border-medium bg-g-secondary text-g-accent-main focus:ring-g-accent-main/40"
              />
              <span>Skip TLS certificate verification</span>
            </label>
            <p className="text-xs text-g-red-text -mt-2">Disables certificate validation. Use only for lab/test instances.</p>
          </>
        )}
        <ErrorBanner error={err} />
      </div>
    </Modal>
  );
}
