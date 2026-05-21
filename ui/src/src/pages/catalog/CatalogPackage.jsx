import { useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import {
  MdArrowBack, MdInventory2, MdLayers, MdTerminal, MdOpenInNew,
  MdDownload, MdExpandMore, MdExpandLess, MdContentCopy, MdCheck, MdCloudDownload,
} from 'react-icons/md';
import { catalog, ApiError } from '../../api/client.js';
import { useCatalogAuth } from '../../contexts/CatalogAuthContext.jsx';
import Spinner from '../../components/Spinner.jsx';
import ErrorBanner from '../../components/ErrorBanner.jsx';
import CopyableCode from '../../components/CopyableCode.jsx';
import MarkdownBlock from '../../components/MarkdownBlock.jsx';
import Badge from '../../components/Badge.jsx';
import Button from '../../components/Button.jsx';
import EmptyState from '../../components/EmptyState.jsx';

function asArray(v, key) {
  if (Array.isArray(v)) return v;
  if (v && Array.isArray(v[key])) return v[key];
  return [];
}

const kindIcon = {
  container: MdInventory2,
  helm:      MdLayers,
  binary:    MdTerminal,
  compose:   MdCloudDownload,
};

const TAB_LABEL = {
  container: 'Container',
  helm:      'Helm',
  binary:    'Binary',
  compose:   'Compose bundle',
};

export default function CatalogPackage() {
  const { slug } = useParams();
  const { session } = useCatalogAuth();
  const [pkg, setPkg] = useState(null);
  const [hostname, setHostname] = useState('artifacts.example.com');
  const [err, setErr] = useState(null);

  useEffect(() => {
    setErr(null);
    setPkg(null);
    catalog.getPackage(slug).then(setPkg).catch(setErr);
    catalog.hostname().then((h) => {
      if (h?.hostname) setHostname(h.hostname);
    }).catch(() => { /* fall back to default */ });
  }, [slug]);

  const tokenId = session?.token_id || session?.tokenId || '<your-token-id>';

  if (err) {
    return (
      <div className="max-w-5xl mx-auto px-6 py-10">
        <ErrorBanner error={err} />
        <div className="mt-4">
          <Link to="/catalog" className="text-sm text-g-text-link hover:underline">
            <MdArrowBack className="inline -mt-0.5" /> Back to catalog
          </Link>
        </div>
      </div>
    );
  }

  if (!pkg) {
    return <div className="max-w-5xl mx-auto px-6 py-16"><Spinner /></div>;
  }

  const Icon = kindIcon[pkg.kind] || MdInventory2;
  const isGH = pkg.source === 'github-release';

  return (
    <div className="max-w-5xl mx-auto px-6 py-10">
      <div className="mb-6">
        <Link to="/catalog" className="text-sm text-g-text-secondary hover:text-g-text inline-flex items-center gap-1">
          <MdArrowBack /> All packages
        </Link>
      </div>

      <header className="flex flex-col sm:flex-row sm:items-start gap-4 pb-6 mb-8 border-b border-g-border-weak">
        <div className="w-14 h-14 shrink-0 rounded bg-g-accent-main/10 text-g-accent-text flex items-center justify-center">
          <Icon className="text-3xl" />
        </div>
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2 mb-1">
            <h1 className="text-2xl font-semibold tracking-tight text-g-text">{pkg.display_name || pkg.slug}</h1>
            <span className="chip-accent">{TAB_LABEL[pkg.kind] || pkg.kind}</span>
            {isGH && <Badge color="purple">GitHub Release</Badge>}
          </div>
          <p className="text-sm text-g-text-secondary">{pkg.description || ''}</p>
          <p className="mt-2 text-xs font-mono text-g-text-disabled">
            {hostname}/{pkg.path}
          </p>
        </div>
      </header>

      {isGH ? (
        <DownloadsSection slug={pkg.slug} hostname={hostname} tokenId={tokenId} />
      ) : (
        <InstallSection pkg={pkg} hostname={hostname} tokenId={tokenId} slug={slug} />
      )}

      {pkg.release_notes_url && (
        <section className="mb-10">
          <h2 className="text-lg font-semibold mb-3">Release notes</h2>
          <a
            href={pkg.release_notes_url}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 text-sm text-g-text-link hover:underline"
          >
            View release notes <MdOpenInNew className="text-base" />
          </a>
        </section>
      )}

      {pkg.install_instructions_md && (
        <section className="mb-10">
          <h2 className="text-lg font-semibold mb-3">Detailed instructions</h2>
          <div className="bg-g-elevated border border-g-border-weak rounded px-5 py-4">
            <MarkdownBlock source={pkg.install_instructions_md} />
          </div>
        </section>
      )}
    </div>
  );
}

// --- OCI install section (unchanged tab UX) ------------------------------

function InstallSection({ pkg, hostname, tokenId, slug }) {
  const [tags, setTags] = useState(null);
  const [tagsErr, setTagsErr] = useState(null);
  const [selectedTag, setSelectedTag] = useState(null);
  const [activeTab, setActiveTab] = useState(pkg.kind);

  useEffect(() => {
    setTags(null);
    setTagsErr(null);
    catalog.listTags(slug)
      .then((res) => {
        const list = asArray(res, 'tags');
        setTags(list);
        if (!selectedTag && list.length) setSelectedTag(list[0]);
      })
      .catch((e) => {
        if (e instanceof ApiError && e.status === 404) setTags([]);
        else setTagsErr(e);
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [slug]);

  const tag = selectedTag || pkg?.default_tag || 'latest';
  const installSnippets = useMemo(
    () => buildOciSnippets({ hostname, path: pkg?.path, tag, tokenId }),
    [hostname, pkg, tag, tokenId],
  );

  const tabs = pkg.kind === 'helm' ? ['helm', 'container'] : pkg.kind === 'binary' ? ['binary'] : ['container', 'helm'];
  const tabSet = new Set(tabs);
  if (!tabSet.has(pkg.kind)) tabs.unshift(pkg.kind);

  return (
    <>
      <section className="mb-10">
        <h2 className="text-lg font-semibold mb-3">Install</h2>

        <div className="border-b border-g-border-weak flex items-center gap-1 mb-4">
          {tabs.map((kind) => (
            <button
              key={kind}
              type="button"
              onClick={() => setActiveTab(kind)}
              className={`px-3 py-2 text-sm font-medium border-b-2 -mb-px transition-colors ${
                activeTab === kind
                  ? 'border-g-accent-main text-g-accent-text'
                  : 'border-transparent text-g-text-secondary hover:text-g-text'
              }`}
            >
              {TAB_LABEL[kind] || kind}
            </button>
          ))}
        </div>

        <CopyableCode value={installSnippets[activeTab] || installSnippets[pkg.kind] || ''} language="shell" />

        <p className="mt-2 text-xs text-g-text-disabled">
          Your secret is never rendered on this page. Substitute{' '}
          <code className="px-1 py-0.5 bg-g-secondary rounded font-mono text-[11px]">&lt;your secret&gt;</code>{' '}
          with the value you used to sign in.
        </p>
      </section>

      <section className="mb-10">
        <h2 className="text-lg font-semibold mb-3">Versions</h2>
        {tagsErr && <ErrorBanner error={tagsErr} className="mb-3" />}
        {tags === null ? (
          <div className="py-4"><Spinner /></div>
        ) : tags.length === 0 ? (
          <div className="text-sm text-g-text-secondary">No tags published yet for this package.</div>
        ) : (
          <div className="flex flex-wrap gap-2">
            {tags.slice(0, 10).map((t) => {
              const isActive = t === selectedTag;
              return (
                <button
                  key={t}
                  type="button"
                  onClick={() => setSelectedTag(t)}
                  className={`px-2 py-1 rounded text-xs font-mono border transition-colors ${
                    isActive
                      ? 'bg-g-accent-main text-white border-g-accent-main'
                      : 'bg-g-secondary text-g-text-secondary border-g-border-weak hover:bg-g-hover hover:text-g-text'
                  }`}
                  title="Copy this version into the install command"
                >
                  {t}
                </button>
              );
            })}
            {tags.length > 10 && (
              <span className="text-xs text-g-text-disabled self-center pl-1">
                + {tags.length - 10} older
              </span>
            )}
          </div>
        )}
      </section>
    </>
  );
}

function buildOciSnippets({ hostname, path, tag, tokenId }) {
  if (!path) return {};
  const reg = `${hostname}/${path}`;
  return {
    container:
`# Authenticate to the gateway (paste your secret when prompted)
docker login ${hostname} -u ${tokenId}
# password: <your secret>

# Pull the image
docker pull ${reg}:${tag}`,

    helm:
`# Authenticate to the OCI registry
helm registry login ${hostname} -u ${tokenId}
# password: <your secret>

# Pull the chart
helm pull oci://${reg} --version ${tag}`,

    binary:
`# Authenticate to the gateway
echo "<your secret>" | oras login ${hostname} -u ${tokenId} --password-stdin

# Pull the artifact
oras pull ${reg}:${tag}`,
  };
}

// --- GitHub-release downloads section ------------------------------------

function DownloadsSection({ slug, hostname, tokenId }) {
  const [data, setData] = useState(null);
  const [err, setErr] = useState(null);

  useEffect(() => {
    setData(null); setErr(null);
    catalog.listDownloads(slug)
      .then((res) => setData(res || {}))
      .catch((e) => {
        if (e instanceof ApiError && e.status === 404) setData({ releases: [] });
        else setErr(e);
      });
  }, [slug]);

  const releases = useMemo(() => {
    // Backend may key the list as `releases` (per the contract) or `tags`
    // (mirroring the discovery JSON in DOWNLOADS.md §4.3). Accept either.
    if (!data) return null;
    const list = asArray(data, 'releases');
    if (list.length) return list;
    return asArray(data, 'tags');
  }, [data]);

  // Find the newest non-prerelease for the `latest` badge.
  const latestIdx = useMemo(() => {
    if (!releases) return -1;
    return releases.findIndex((r) => !r.prerelease);
  }, [releases]);

  return (
    <section className="mb-10">
      <h2 className="text-lg font-semibold mb-2">Downloads</h2>
      <p className="text-sm text-g-text-secondary mb-5">
        Pick a release, then click <span className="font-medium text-g-text">Download</span>.
        The file streams directly from GitHub via a one-shot signed link &mdash; no extra login required.
      </p>

      {err && <ErrorBanner error={err} className="mb-3" />}

      {releases === null ? (
        <div className="py-8"><Spinner label="Loading releases" /></div>
      ) : releases.length === 0 ? (
        <EmptyState
          icon={MdDownload}
          title="No releases published yet"
          description="When the upstream repository publishes a release, its assets will appear here."
        />
      ) : (
        <div className="space-y-2">
          {releases.map((r, i) => (
            <ReleaseRow
              key={r.tag || i}
              release={r}
              slug={slug}
              hostname={hostname}
              tokenId={tokenId}
              defaultOpen={i === 0}
              isLatest={i === latestIdx}
            />
          ))}
        </div>
      )}

      <p className="mt-4 text-xs text-g-text-disabled">
        Want to script this? Use a customer token (created from{' '}
        <Link to="/catalog/credentials" className="text-g-text-link hover:underline">Credentials</Link>)
        and the <code className="px-1 py-0.5 bg-g-secondary rounded font-mono text-[11px]">Copy curl</code>{' '}
        snippet next to each asset.
      </p>
    </section>
  );
}

function ReleaseRow({ release, slug, hostname, tokenId, defaultOpen, isLatest }) {
  const [open, setOpen] = useState(!!defaultOpen);
  const assets = asArray(release, 'assets');
  const published = release.published_at ? new Date(release.published_at) : null;
  const isPre = !!release.prerelease;

  return (
    <div className="border border-g-border-weak rounded bg-g-primary">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="w-full flex items-center gap-3 px-4 py-3 text-left hover:bg-g-hover/40 transition-colors rounded"
        aria-expanded={open}
      >
        <span className="text-g-text-secondary">
          {open ? <MdExpandLess className="text-xl" /> : <MdExpandMore className="text-xl" />}
        </span>
        <div className="flex-1 min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm font-medium text-g-text">{release.tag || '—'}</span>
            {release.name && release.name !== release.tag && (
              <span className="text-sm text-g-text-secondary truncate">{release.name}</span>
            )}
            {isLatest && <Badge color="green">latest</Badge>}
            {isPre && <Badge color="yellow">pre-release</Badge>}
          </div>
          <div className="text-xs text-g-text-disabled mt-0.5">
            {published ? published.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' }) : 'unpublished'}
            {assets.length > 0 && <> &middot; {assets.length} asset{assets.length === 1 ? '' : 's'}</>}
          </div>
        </div>
      </button>

      {open && (
        <div className="border-t border-g-border-weak">
          {assets.length === 0 ? (
            <div className="px-4 py-3 text-sm text-g-text-secondary">No assets in this release.</div>
          ) : (
            <ul className="divide-y divide-g-border-weak">
              {assets.map((a) => (
                <AssetRow
                  key={a.name}
                  asset={a}
                  slug={slug}
                  tag={release.tag}
                  hostname={hostname}
                  tokenId={tokenId}
                />
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}

function AssetRow({ asset, slug, tag, hostname, tokenId }) {
  const [signing, setSigning] = useState(false);
  const [signErr, setSignErr] = useState(null);
  const [copied, setCopied] = useState(false);

  const onDownload = async () => {
    if (signing) return;
    setSigning(true); setSignErr(null);
    try {
      const res = await catalog.signDownload(slug, tag, asset.name);
      const url = res?.url;
      if (!url) throw new Error('Server did not return a signed URL.');
      window.location.assign(url);
    } catch (e) {
      setSignErr(e);
      setSigning(false);
    }
  };

  const curl = useMemo(
    () => `curl -fLO -u ${tokenId}:<your-secret> https://${hostname}/download/${slug}/${tag}/${asset.name}`,
    [tokenId, hostname, slug, tag, asset.name],
  );

  const onCopyCurl = async () => {
    try {
      await navigator.clipboard.writeText(curl);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {/* clipboard blocked */}
  };

  return (
    <li className="px-4 py-3 grid grid-cols-12 gap-3 items-center">
      <div className="col-span-12 sm:col-span-6 min-w-0">
        <div className="font-mono text-sm text-g-text truncate" title={asset.name}>{asset.name}</div>
        {asset.content_type && (
          <div className="text-[11px] text-g-text-disabled mt-0.5">{asset.content_type}</div>
        )}
        {signErr && (
          <div className="text-xs text-g-red-text mt-1">{signErr.message || String(signErr)}</div>
        )}
      </div>
      <div className="col-span-6 sm:col-span-2 text-xs text-g-text-secondary">
        {humanSize(asset.size)}
      </div>
      <div className="col-span-6 sm:col-span-4 flex items-center justify-end gap-2">
        <button
          type="button"
          onClick={onCopyCurl}
          title="Copy curl command"
          className="inline-flex items-center gap-1 px-2 py-1 rounded text-[11px] font-medium border border-g-border-weak text-g-text-secondary hover:text-g-text hover:bg-g-hover transition-colors"
        >
          {copied ? <MdCheck className="text-g-green-text" /> : <MdContentCopy />}
          {copied ? 'Copied' : 'Copy curl'}
        </button>
        <Button
          variant="primary"
          size="sm"
          icon={<MdDownload />}
          loading={signing}
          onClick={onDownload}
        >
          {signing ? 'Preparing…' : 'Download'}
        </Button>
      </div>
    </li>
  );
}

function humanSize(bytes) {
  if (bytes == null || Number.isNaN(Number(bytes))) return '—';
  const n = Number(bytes);
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024, i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v >= 10 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}
