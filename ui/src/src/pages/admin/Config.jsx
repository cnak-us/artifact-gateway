import { useMemo, useRef, useState } from 'react';
import {
  MdCloudDownload, MdFileOpen, MdClear, MdRefresh,
  MdPlayArrow, MdRocketLaunch, MdInfoOutline, MdSettings,
} from 'react-icons/md';
import { admin } from '../../api/client.js';
import { useToast } from '../../components/Toast.jsx';
import Button from '../../components/Button.jsx';
import ErrorBanner from '../../components/ErrorBanner.jsx';
import Badge from '../../components/Badge.jsx';
import Textarea from '../../components/Textarea.jsx';
import EmptyState from '../../components/EmptyState.jsx';
import Customization from './Customization.jsx';

const TABS = [
  { id: 'state',         label: 'State' },
  { id: 'customization', label: 'Customization' },
];

function TabBar({ tabs, value, onChange }) {
  return (
    <div className="border-b border-g-border-weak">
      <div className="flex gap-1 -mb-px">
        {tabs.map((t) => {
          const active = t.id === value;
          return (
            <button
              key={t.id}
              type="button"
              onClick={() => onChange(t.id)}
              className={
                'px-4 py-2 text-sm font-medium border-b-2 transition-colors ' +
                (active
                  ? 'border-g-accent-main text-g-text'
                  : 'border-transparent text-g-text-secondary hover:text-g-text hover:border-g-border-medium')
              }
            >
              {t.label}
            </button>
          );
        })}
      </div>
    </div>
  );
}

export default function Config() {
  // Persist the active tab in the URL hash so reloads keep position. (#state | #customization)
  const initial = (typeof window !== 'undefined' && window.location.hash.replace('#', '')) || 'state';
  const [tab, setTab] = useState(TABS.some((t) => t.id === initial) ? initial : 'state');
  const onChange = (id) => {
    setTab(id);
    if (typeof window !== 'undefined') window.history.replaceState(null, '', '#' + id);
  };
  return (
    <div className="space-y-4">
      <TabBar tabs={TABS} value={tab} onChange={onChange} />
      {tab === 'state'         && <StateConfig />}
      {tab === 'customization' && <Customization />}
    </div>
  );
}

const TEMPLATE = `apiVersion: artifact-gateway.cnak.us/v1
kind: ArtifactGatewayConfig
metadata:
  name: default
spec: {}
`;

const FILENAME = 'artifact-gateway-config.yaml';

const ACTION_BADGE = {
  create: { color: 'green',  label: 'create' },
  update: { color: 'blue',   label: 'update' },
  noop:   { color: 'gray',   label: 'noop' },
  delete: { color: 'red',    label: 'delete' },
};

// Mirror the asArray helper in Licenses.jsx — accept either a raw array OR
// a wrapped {key: [...]} response shape so a backend reshape can't crash us.
function asArray(v, key) {
  if (Array.isArray(v)) return v;
  if (v && Array.isArray(v[key])) return v[key];
  return [];
}

// Quick non-crypto hash so we can flag "editor unchanged since last dry-run".
function hashContent(s) {
  let h = 5381;
  for (let i = 0; i < s.length; i++) h = ((h << 5) + h + s.charCodeAt(i)) | 0;
  return String(h);
}

function filenameFromDisposition(value) {
  if (!value) return null;
  const m = /filename\*?=(?:UTF-8''|")?([^";]+)/i.exec(value);
  if (!m) return null;
  try { return decodeURIComponent(m[1].replace(/"$/, '')); } catch { return m[1]; }
}

function triggerDownload(text, filename) {
  const blob = new Blob([text], { type: 'text/yaml' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  // Give the browser a tick to start the download before we revoke.
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function StateConfig() {
  const toast = useToast();
  const fileInputRef = useRef(null);

  const [editor, setEditor]   = useState('');
  const [prune, setPrune]     = useState(false);
  const [busy, setBusy]       = useState(null);     // 'export' | 'dryrun' | 'apply' | null
  const [err, setErr]         = useState(null);
  const [report, setReport]   = useState(null);
  const [lastOkDryHash, setLastOkDryHash] = useState(null);

  const editorHash    = useMemo(() => hashContent(editor), [editor]);
  const dryRunIsFresh = lastOkDryHash !== null && lastOkDryHash === editorHash;
  const applyDisabled = !editor.trim() || !dryRunIsFresh || busy !== null;

  const onExport = async () => {
    setErr(null);
    setBusy('export');
    try {
      const res = await admin.configExport();
      if (!res.ok) {
        const body = await res.text().catch(() => '');
        throw new Error(body || `Export failed (${res.status})`);
      }
      const text = await res.text();
      const fname = filenameFromDisposition(res.headers.get('content-disposition')) || FILENAME;
      triggerDownload(text, fname);
      toast.success(`Downloaded ${fname}`);
    } catch (e) { setErr(e); }
    finally { setBusy(null); }
  };

  const onLoadCurrent = async () => {
    setErr(null);
    setBusy('export');
    try {
      const res = await admin.configExport();
      if (!res.ok) {
        const body = await res.text().catch(() => '');
        throw new Error(body || `Load failed (${res.status})`);
      }
      const text = await res.text();
      setEditor(text);
      setLastOkDryHash(null);
      setReport(null);
      toast.info('Loaded current configuration into editor');
    } catch (e) { setErr(e); }
    finally { setBusy(null); }
  };

  const onFileOpen = async (e) => {
    const file = e.target.files?.[0];
    if (!file) return;
    try {
      const text = await file.text();
      setEditor(text);
      setLastOkDryHash(null);
      setReport(null);
    } catch (readErr) { setErr(readErr); }
    finally {
      // Reset so picking the same file twice still fires onChange.
      if (fileInputRef.current) fileInputRef.current.value = '';
    }
  };

  const onClear = () => {
    setEditor('');
    setLastOkDryHash(null);
    setReport(null);
    setErr(null);
  };

  const onInsertTemplate = () => {
    setEditor(TEMPLATE);
    setLastOkDryHash(null);
    setReport(null);
  };

  const runApply = async ({ dryRun } = {}) => {
    if (!editor.trim()) {
      setErr(new Error('Manifest is empty.'));
      return;
    }
    setErr(null);
    setBusy(dryRun ? 'dryrun' : 'apply');
    try {
      const res = await admin.configApply(editor, { dryRun, prune });
      const items  = asArray(res, 'items');
      const errors = asArray(res, 'errors');
      setReport({ dry_run: dryRun, items, errors });
      if (dryRun) {
        setLastOkDryHash(editorHash);
        toast.success(
          errors.length
            ? `Dry run complete — ${errors.length} error${errors.length === 1 ? '' : 's'}`
            : `Dry run complete — ${items.length} change${items.length === 1 ? '' : 's'}`,
        );
      } else {
        toast.success(
          errors.length
            ? `Apply complete with ${errors.length} error${errors.length === 1 ? '' : 's'}`
            : `Apply complete — ${items.length} change${items.length === 1 ? '' : 's'} written`,
        );
        // Force a fresh dry run before the next apply.
        setLastOkDryHash(null);
      }
    } catch (e) { setErr(e); }
    finally { setBusy(null); }
  };

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-6">
        <div>
          <h1 className="text-xl font-semibold">Configuration</h1>
          <p className="text-sm text-g-text-secondary max-w-2xl">
            Declarative <span className="font-mono">kubectl apply</span>–style surface: export current state, edit, dry-run, apply.
          </p>
        </div>
        <div className="flex flex-col items-end gap-1">
          <Button
            variant="primary"
            size="lg"
            icon={<MdCloudDownload />}
            loading={busy === 'export'}
            disabled={busy !== null}
            onClick={onExport}
          >
            Download current configuration
          </Button>
          <div className="text-[11px] text-g-text-secondary max-w-xs text-right">
            Secrets (PATs, OIDC client secrets, passwords) are exported as
            {' '}<span className="font-mono">&lt;redacted&gt;</span> — re-supply them
            before re-applying.
          </div>
        </div>
      </div>

      <ErrorBanner error={err} />

      <div className="rounded border border-g-border-weak bg-g-primary">
        <div className="px-4 py-3 border-b border-g-border-weak flex items-center justify-between flex-wrap gap-2">
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-semibold text-g-text">Manifest editor</h2>
            <Badge color="gray">YAML</Badge>
          </div>
          <div className="flex items-center gap-1">
            <Button
              variant="outline"
              size="sm"
              icon={<MdRefresh />}
              onClick={onLoadCurrent}
              loading={busy === 'export'}
              disabled={busy !== null}
            >
              Load current
            </Button>
            <Button
              variant="outline"
              size="sm"
              icon={<MdFileOpen />}
              onClick={() => fileInputRef.current?.click()}
              disabled={busy !== null}
            >
              Open file…
            </Button>
            <input
              ref={fileInputRef}
              type="file"
              accept=".yaml,.yml,.json,text/yaml,application/json"
              className="hidden"
              onChange={onFileOpen}
            />
            <Button
              variant="ghost"
              size="sm"
              icon={<MdClear />}
              onClick={onClear}
              disabled={busy !== null || !editor}
            >
              Clear
            </Button>
          </div>
        </div>

        <div className="p-4 space-y-3">
          {editor === '' && (
            <div className="flex items-center justify-between gap-3 px-3 py-2 rounded border border-dashed border-g-border-medium bg-g-secondary/40 text-xs text-g-text-secondary">
              <span>
                Empty editor — paste a manifest, load the current configuration,
                or start from a blank template.
              </span>
              <Button variant="ghost" size="sm" onClick={onInsertTemplate}>
                Insert template
              </Button>
            </div>
          )}
          <Textarea
            mono
            rows={30}
            value={editor}
            onChange={(e) => {
              setEditor(e.target.value);
              // Editor changed — Apply must wait for a fresh dry run.
              if (lastOkDryHash !== null) setLastOkDryHash(null);
            }}
            placeholder={TEMPLATE}
            spellCheck={false}
          />
        </div>

        <div className="px-4 py-3 border-t border-g-border-weak bg-g-secondary/40 flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-5 text-sm">
            <label
              className="inline-flex items-center gap-2 cursor-pointer select-none"
              title="Removes anything not in the manifest. Use carefully."
            >
              <input
                type="checkbox"
                className="accent-g-accent-main"
                checked={prune}
                onChange={(e) => setPrune(e.target.checked)}
              />
              <span className="text-g-text">Prune</span>
              <MdInfoOutline className="text-g-text-secondary" />
              {prune && (
                <Badge color="orange">removes anything not in manifest</Badge>
              )}
            </label>
          </div>

          <div className="flex items-center gap-2">
            {!dryRunIsFresh && editor.trim() && (
              <span className="text-[11px] text-g-text-secondary">
                Run a dry run to enable Apply
              </span>
            )}
            <Button
              variant="outline"
              icon={<MdPlayArrow />}
              loading={busy === 'dryrun'}
              disabled={busy !== null || !editor.trim()}
              onClick={() => runApply({ dryRun: true })}
            >
              Dry run
            </Button>
            <Button
              variant="primary"
              icon={<MdRocketLaunch />}
              loading={busy === 'apply'}
              disabled={applyDisabled}
              onClick={() => runApply({ dryRun: false })}
              title={!dryRunIsFresh ? 'Run a successful dry run on the current editor contents first.' : ''}
            >
              Apply
            </Button>
          </div>
        </div>
      </div>

      {report && <ReportPanel report={report} prune={prune} />}
    </div>
  );
}

function ReportPanel({ report, prune }) {
  const items  = report.items  || [];
  const errors = report.errors || [];

  const grouped = useMemo(() => {
    const byKind = new Map();
    for (const it of items) {
      const k = it.kind || '—';
      if (!byKind.has(k)) byKind.set(k, []);
      byKind.get(k).push(it);
    }
    for (const list of byKind.values()) {
      list.sort((a, b) => String(a.name || '').localeCompare(String(b.name || '')));
    }
    return [...byKind.entries()].sort(([a], [b]) => a.localeCompare(b));
  }, [items]);

  const counts = useMemo(() => {
    const c = { create: 0, update: 0, noop: 0, delete: 0 };
    for (const it of items) if (c[it.action] !== undefined) c[it.action]++;
    return c;
  }, [items]);

  const isEmpty = items.length === 0 && errors.length === 0;

  return (
    <div className="rounded border border-g-border-weak bg-g-primary">
      <div className="px-4 py-3 border-b border-g-border-weak flex items-center justify-between flex-wrap gap-2">
        <div className="flex items-center gap-2">
          <h2 className="text-sm font-semibold text-g-text">Apply report</h2>
          {report.dry_run
            ? <Badge color="blue">dry run · no writes</Badge>
            : <Badge color="green">applied</Badge>}
          {prune && !report.dry_run && <Badge color="orange">prune on</Badge>}
        </div>
        <div className="flex items-center gap-2 text-xs">
          {counts.create > 0 && <Badge color="green">create · {counts.create}</Badge>}
          {counts.update > 0 && <Badge color="blue">update · {counts.update}</Badge>}
          {counts.noop   > 0 && <Badge color="gray">noop · {counts.noop}</Badge>}
          {counts.delete > 0 && <Badge color="red">delete · {counts.delete}</Badge>}
        </div>
      </div>

      {errors.length > 0 && (
        <div className="px-4 py-3 border-b border-g-border-weak">
          <ErrorBanner
            error={`${errors.length} error${errors.length === 1 ? '' : 's'} reported — see Notes column below.`}
          />
        </div>
      )}

      {isEmpty ? (
        <EmptyState
          icon={MdSettings}
          title="Nothing to do"
          description="The manifest matches current state — no creates, updates, or deletes are required."
        />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm border-collapse">
            <thead>
              <tr className="bg-g-secondary">
                {['Kind', 'Name', 'Action', 'Diff', 'Notes'].map((h) => (
                  <th
                    key={h}
                    className="text-left text-[11px] font-semibold uppercase tracking-wider text-g-text-secondary px-3 py-2 border-b border-g-border-weak"
                  >
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {grouped.map(([kind, rows]) => (
                <KindGroup key={kind} kind={kind} rows={rows} errors={errors} />
              ))}
              {grouped.length === 0 && errors.length > 0 && (
                <ErrorOnlyRows errors={errors} />
              )}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function KindGroup({ kind, rows, errors }) {
  return (
    <>
      <tr className="bg-g-secondary/60">
        <td colSpan={5} className="px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-g-text-secondary">
          {kind}
        </td>
      </tr>
      {rows.map((r, i) => {
        const action = ACTION_BADGE[r.action] || { color: 'gray', label: r.action || '—' };
        const diffs  = asArray(r.diff, 'diff');
        const matchedErr = errors.find((e) => e.kind === r.kind && e.name === r.name);
        return (
          <tr key={`${kind}-${r.name}-${i}`} className="border-b border-g-border-weak last:border-b-0">
            <td className="px-3 py-2 align-top text-g-text-secondary text-xs">{r.kind || '—'}</td>
            <td className="px-3 py-2 align-top font-mono text-xs text-g-text">{r.name || '—'}</td>
            <td className="px-3 py-2 align-top">
              <Badge color={action.color}>{action.label}</Badge>
            </td>
            <td className="px-3 py-2 align-top">
              {diffs.length === 0
                ? <span className="text-g-text-disabled text-xs">—</span>
                : (
                  <div className="flex flex-wrap gap-1">
                    {diffs.map((f) => (
                      <span
                        key={String(f)}
                        className="font-mono text-[11px] px-1.5 py-0.5 rounded bg-g-secondary text-g-text-secondary border border-g-border-weak"
                      >
                        {String(f)}
                      </span>
                    ))}
                  </div>
                )}
            </td>
            <td className="px-3 py-2 align-top">
              {matchedErr
                ? <span className="text-xs text-g-red-text">{matchedErr.message}</span>
                : <span className="text-xs text-g-text-disabled">—</span>}
            </td>
          </tr>
        );
      })}
    </>
  );
}

function ErrorOnlyRows({ errors }) {
  return (
    <>
      {errors.map((e, i) => (
        <tr key={`err-${i}`} className="border-b border-g-border-weak last:border-b-0">
          <td className="px-3 py-2 text-g-text-secondary text-xs">{e.kind || '—'}</td>
          <td className="px-3 py-2 font-mono text-xs text-g-text">{e.name || '—'}</td>
          <td className="px-3 py-2"><Badge color="red">error</Badge></td>
          <td className="px-3 py-2 text-g-text-disabled text-xs">—</td>
          <td className="px-3 py-2 text-xs text-g-red-text">{e.message || 'failed'}</td>
        </tr>
      ))}
    </>
  );
}

