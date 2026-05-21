import { useEffect, useMemo, useState } from 'react';
import { MdAdd, MdEdit, MdDelete, MdInventory2, MdNetworkCheck } from 'react-icons/md';
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
import ProbeResultModal from '../../components/ProbeResultModal.jsx';

function asArray(v, key) {
  if (Array.isArray(v)) return v;
  if (v && Array.isArray(v[key])) return v[key];
  return [];
}

const EMPTY = {
  slug: '', path: '', upstream_repo: '', upstream_credential_id: '',
  kind: 'container', display_name: '', description: '',
  release_notes_url: '', install_instructions_md: '',
  source: 'oci',
  github_repo: '', release_pattern: 'latest', asset_pattern: '*',
};

const SOURCE_OPTS = [
  { value: 'oci',            label: 'OCI registry (GHCR / Docker Hub / Quay / GitLab / Gitea / Harbor / ECR / GAR / ACR / Artifactory)' },
  { value: 'github-release', label: 'GitHub Release (binary download)' },
  { value: 'gitlab-release', label: 'GitLab Release (binary download)' },
];

const SOURCE_HINTS = {
  'oci':            "Proxies /v2/* to an upstream OCI registry. The specific vendor is determined by the upstream credential you pick — any non-API credential works here.",
  'github-release': "Fetches release assets from the GitHub Releases API. Requires a 'github-api' upstream credential.",
  'gitlab-release': "Fetches release links from the GitLab Releases API. Requires a 'gitlab-api' upstream credential. The 'project path' below is the full group/subgroup/project path.",
};

// Credential Kinds that serve OCI manifests/blobs through Upstream.Proxy().
// Everything except 'github-api' (which targets the Releases REST API).
// Keep in sync with validUpstreamCredKinds in server/admin.go.
const OCI_CRED_KINDS = new Set([
  'ghcr', 'oci-basic', 'dockerhub', 'quay', 'gitlab',
  'ecr', 'gar', 'acr-aad',
]);

const OCI_KIND_OPTS = [
  { value: 'container', label: 'container' },
  { value: 'helm',      label: 'helm' },
  { value: 'binary',    label: 'binary' },
];

const GH_REPO_RE = /^[^/\s]+\/[^/\s]+$/;
// GitLab project paths can be deeper than two segments (group/subgroup/project),
// so we accept any non-empty slash-separated path of two-or-more segments.
const GL_REPO_RE = /^[^\s/][^\s]*\/[^\s/][^\s]*(?:\/[^\s/][^\s]*)*$/;

// SOURCE_TO_CRED_KIND maps a release-style package source to the credential
// Kind it requires. Used by the credential filter when source is a release.
const SOURCE_TO_CRED_KIND = {
  'github-release': 'github-api',
  'gitlab-release': 'gitlab-api',
};

export default function Packages() {
  const toast = useToast();
  const confirm = useConfirm();
  const [items, setItems] = useState(null);
  const [creds, setCreds] = useState([]);
  const [err, setErr] = useState(null);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState(null);
  const [probe, setProbe] = useState({ open: false, title: '', loading: false, result: null, error: null });

  const load = async () => {
    setErr(null);
    try {
      const [pkgs, ucs] = await Promise.all([
        admin.listPackages(),
        admin.listUpstreamCredentials(),
      ]);
      setItems(asArray(pkgs, 'packages'));
      setCreds(asArray(ucs, 'credentials'));
    } catch (e) { setErr(e); }
  };
  useEffect(() => { load(); }, []);

  const onCreate = () => { setEditing({ ...EMPTY }); setOpen(true); };
  const onEdit = (p) => { setEditing({ ...EMPTY, ...p }); setOpen(true); };
  const onDelete = async (p) => {
    const ok = await confirm({
      title: 'Delete package?',
      message: `"${p.slug}" will be deleted.`,
      confirmLabel: 'Delete package',
      danger: true,
    });
    if (!ok) return;
    try { await admin.deletePackage(p.id); toast.success(`Deleted "${p.slug}"`); await load(); }
    catch (e) { toast.error(e.message); }
  };

  const onProbe = async (p) => {
    setProbe({ open: true, title: `Probe "${p.slug}" package`, loading: true, result: null, error: null });
    try {
      const res = await admin.probePackage(p.id);
      setProbe((s) => ({ ...s, loading: false, result: res, error: null }));
    } catch (e) {
      setProbe((s) => ({ ...s, loading: false, result: null, error: e }));
    }
  };

  const columns = [
    {
      key: 'slug',
      header: 'Slug',
      render: (p) => (
        <div>
          <div className="font-medium text-g-text">{p.display_name || p.slug}</div>
          <div className="text-xs text-g-text-secondary">{p.slug}</div>
        </div>
      ),
    },
    {
      key: 'source',
      header: 'Source',
      render: (p) => {
        const src = p.source || 'oci';
        if (src === 'github-release') return <Badge color="purple">GitHub Release</Badge>;
        if (src === 'gitlab-release') return <Badge color="orange">GitLab Release</Badge>;
        return <Badge color="gray">OCI</Badge>;
      },
    },
    { key: 'kind', header: 'Kind', render: (p) => <Badge>{p.kind}</Badge> },
    {
      key: 'upstream',
      header: 'Upstream',
      render: (p) => {
        const src = p.source || 'oci';
        const isRel = src === 'github-release' || src === 'gitlab-release';
        return (
          <span className="font-mono text-xs">
            {isRel ? (p.github_repo || p.upstream_repo || '—') : (p.upstream_repo || '—')}
          </span>
        );
      },
    },
    { key: 'cred', header: 'Credential', render: (p) => <span className="text-xs">{credName(creds, p.upstream_credential_id)}</span> },
    {
      key: 'actions',
      header: '',
      className: 'text-right',
      render: (p) => (
        <div className="flex gap-1 justify-end">
          <IconButton icon={<MdNetworkCheck />} label="Probe" onClick={() => onProbe(p)} />
          <IconButton icon={<MdEdit />} label="Edit" onClick={() => onEdit(p)} />
          <IconButton icon={<MdDelete />} label="Delete" variant="danger" onClick={() => onDelete(p)} />
        </div>
      ),
    },
  ];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Packages</h1>
          <p className="text-sm text-g-text-secondary">OCI artifacts and GitHub / GitLab Releases proxied by this gateway.</p>
        </div>
        <Button variant="primary" icon={<MdAdd />} onClick={onCreate}>New package</Button>
      </div>

      <ErrorBanner error={err} />

      {items === null ? <Spinner label="Loading packages" /> : items.length === 0 ? (
        <EmptyState
          icon={MdInventory2}
          title="No packages yet"
          description="Register an OCI artifact (container, helm chart, or binary) or a GitHub Release for this gateway to proxy."
          action={<Button variant="primary" icon={<MdAdd />} onClick={onCreate}>New package</Button>}
        />
      ) : (
        <Table columns={columns} rows={items} />
      )}

      <PackageModal
        open={open}
        onClose={() => setOpen(false)}
        initial={editing}
        creds={creds}
        onSaved={(msg) => { setOpen(false); toast.success(msg); load(); }}
      />

      <ProbeResultModal
        open={probe.open}
        onClose={() => setProbe((s) => ({ ...s, open: false }))}
        title={probe.title}
        loading={probe.loading}
        result={probe.result}
        error={probe.error}
      />
    </div>
  );
}

function credName(creds, id) {
  const c = creds.find((x) => x.id === id);
  return c ? c.name : id || '—';
}

function PackageModal({ open, onClose, initial, creds, onSaved }) {
  const [form, setForm] = useState(initial || EMPTY);
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) return;
    const base = { ...EMPTY, ...(initial || {}) };
    if (!base.source) base.source = 'oci';
    setForm(base);
    setErr(null);
  }, [initial, open]);

  const set = (k) => (e) => setForm((f) => ({ ...f, [k]: e.target.value }));

  // When the source changes, snap kind + cleared-out fields to sensible defaults.
  const setSource = (e) => {
    const next = e.target.value;
    setForm((f) => {
      if (next === 'github-release' || next === 'gitlab-release') {
        return {
          ...f,
          source: next,
          kind: f.kind && f.kind !== 'container' && f.kind !== 'helm' ? f.kind : 'binary',
          release_pattern: f.release_pattern || 'latest',
          asset_pattern: f.asset_pattern || '*',
          upstream_credential_id: '',
        };
      }
      return {
        ...f,
        source: next,
        kind: f.kind && f.kind !== 'compose' ? f.kind : 'container',
        upstream_credential_id: '',
      };
    });
  };

  const isGH = form.source === 'github-release';
  const isGL = form.source === 'gitlab-release';
  const isRelease = isGH || isGL;
  const releaseCredKind = SOURCE_TO_CRED_KIND[form.source];
  const repoRe = isGL ? GL_REPO_RE : GH_REPO_RE;
  const repoLabel = isGL ? 'GitLab project path *' : 'GitHub repo *';
  const repoPlaceholder = isGL ? 'group/subgroup/project' : 'cnak-us/cnak';
  const repoHint = isGL
    ? 'Full path on the configured GitLab host. Supports nested subgroups.'
    : 'owner/repo on github.com';

  // Filter credentials by kind matching the chosen source. Release sources
  // pin to their API kind; OCI sources accept any kind that serves /v2/*.
  const filteredCreds = useMemo(() => {
    return creds.filter((c) => {
      const k = c.kind || 'ghcr';
      if (isRelease) return k === releaseCredKind;
      return OCI_CRED_KINDS.has(k);
    });
  }, [creds, isRelease, releaseCredKind]);

  const credOpts = filteredCreds.map((c) => {
    const kind = c.kind || 'ghcr';
    const endpointSuffix = c.endpoint ? ` — ${c.endpoint.replace(/^https?:\/\//, '')}` : '';
    return { value: c.id, label: `${c.name} · ${kind}${endpointSuffix}` };
  });

  const ghRepoErr = isRelease && form.github_repo && !repoRe.test(form.github_repo.trim())
    ? (isGL ? 'Must look like group/project (subgroups allowed)' : 'Must look like owner/repo')
    : null;

  const canSave = (() => {
    if (!form.slug || !form.path || !form.upstream_credential_id) return false;
    if (isRelease) {
      if (!form.github_repo || !repoRe.test(form.github_repo.trim())) return false;
    } else {
      if (!form.upstream_repo) return false;
    }
    return true;
  })();

  const save = async () => {
    setErr(null); setBusy(true);
    try {
      const body = { ...form };
      if (isRelease) {
        body.source = form.source;
        body.github_repo = body.github_repo.trim();
        body.release_pattern = (body.release_pattern || 'latest').trim();
        body.asset_pattern = (body.asset_pattern || '*').trim();
        // The server's required-field check on upstream_repo also fires for
        // release sources — mirror github_repo so a release package isn't
        // rejected for a field the release path doesn't actually use.
        if (!body.upstream_repo) body.upstream_repo = body.github_repo;
      } else {
        body.source = 'oci';
        // Strip release-only fields so we don't send noise to the OCI path.
        delete body.github_repo;
        delete body.release_pattern;
        delete body.asset_pattern;
      }
      if (form.id) {
        await admin.updatePackage(form.id, body);
        onSaved('Package updated');
      } else {
        await admin.createPackage(body);
        onSaved('Package created');
      }
    } catch (e) { setErr(e); }
    finally { setBusy(false); }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={form.id ? 'Edit package' : 'New package'}
      size="lg"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!canSave} onClick={save}>{busy ? 'Saving…' : 'Save'}</Button>
        </>
      }
    >
      <div className="grid grid-cols-2 gap-3">
        <Input label="Slug *" value={form.slug} onChange={set('slug')} placeholder="cnak-core" />
        <Select
          label="Source *"
          value={form.source}
          onChange={setSource}
          options={SOURCE_OPTS}
          hint={SOURCE_HINTS[form.source]}
        />

        <Input
          label="Path *"
          value={form.path}
          onChange={set('path')}
          placeholder={isRelease ? 'cnak' : 'cnak-us/cnak-core'}
          hint="Slug-style path under the gateway hostname (no leading slash). Used in download URLs."
        />

        {isRelease ? (
          <Select
            label="Kind *"
            value={form.kind || 'binary'}
            onChange={set('kind')}
            options={[
              { value: 'binary', label: 'binary' },
              { value: 'compose', label: 'compose' },
            ]}
          />
        ) : (
          <Select label="Kind *" value={form.kind} onChange={set('kind')} options={OCI_KIND_OPTS} />
        )}

        {isRelease ? (
          <>
            <Input
              label={repoLabel}
              value={form.github_repo}
              onChange={set('github_repo')}
              placeholder={repoPlaceholder}
              error={ghRepoErr}
              hint={repoHint}
            />
            <Input
              label="Release pattern"
              value={form.release_pattern}
              onChange={set('release_pattern')}
              placeholder="latest"
              hint="latest, a specific tag (v1.2.3), or a semver constraint."
            />
            <Input
              label="Asset pattern"
              value={form.asset_pattern}
              onChange={set('asset_pattern')}
              placeholder="cnak-*-linux-amd64*, checksums.txt"
              hint="Comma-separated globs. Asset is exposed if ANY pattern matches (* and ? supported). Default * matches all."
            />
          </>
        ) : (
          <Input
            label="Upstream repo *"
            value={form.upstream_repo}
            onChange={set('upstream_repo')}
            placeholder="ghcr.io/cnak-us/cnak-core"
          />
        )}

        <Select
          label="Upstream credential *"
          value={form.upstream_credential_id}
          onChange={set('upstream_credential_id')}
          placeholder={
            credOpts.length === 0
              ? (isRelease ? `— no ${releaseCredKind} credentials —` : '— no OCI credentials —')
              : '— select —'
          }
          options={credOpts}
          hint={
            credOpts.length === 0
              ? (isRelease
                  ? `No '${releaseCredKind}' credentials yet. Add one first.`
                  : "No OCI-compatible credentials yet. Add a ghcr / oci-basic / dockerhub / quay / gitlab / ecr / gar / acr-aad credential first.")
              : (isRelease
                  ? `Filtered to '${releaseCredKind}' credentials.`
                  : "Filtered to OCI-compatible credentials (ghcr / oci-basic / dockerhub / quay / gitlab / ecr / gar / acr-aad).")
          }
        />
        <Input label="Display name" value={form.display_name} onChange={set('display_name')} />

        <div className="col-span-2">
          <Textarea label="Description" value={form.description} onChange={set('description')} rows={2} />
        </div>
        <div className="col-span-2">
          <Input label="Release notes URL" value={form.release_notes_url} onChange={set('release_notes_url')} placeholder="https://…" />
        </div>
        <div className="col-span-2">
          <Textarea
            label={isRelease ? 'Extra notes (markdown)' : 'Install instructions (markdown)'}
            mono
            rows={6}
            value={form.install_instructions_md}
            onChange={set('install_instructions_md')}
          />
        </div>
      </div>
      <div className="mt-3"><ErrorBanner error={err} /></div>
    </Modal>
  );
}
