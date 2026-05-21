import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { MdArrowForward, MdDownload, MdInventory2, MdLayers, MdTerminal } from 'react-icons/md';
import { catalog } from '../../api/client.js';
import { useCatalogAuth } from '../../contexts/CatalogAuthContext.jsx';
import Spinner from '../../components/Spinner.jsx';
import ErrorBanner from '../../components/ErrorBanner.jsx';
import EmptyState from '../../components/EmptyState.jsx';
import { brand } from '../../brand/index.js';

const kindIcon = {
  container: MdInventory2,
  helm:      MdLayers,
  binary:    MdTerminal,
};

const kindLabel = {
  container: 'Container image',
  helm:      'Helm chart',
  binary:    'Binary',
};

export default function CatalogHome() {
  const { session } = useCatalogAuth();
  const [items, setItems] = useState(null);
  const [err, setErr] = useState(null);

  useEffect(() => {
    catalog.listPackages()
      .then((list) => {
        // Accept either a raw array or a {packages: [...]} wrapper.
        const arr = Array.isArray(list)
          ? list
          : Array.isArray(list?.packages)
            ? list.packages
            : [];
        setItems(arr);
      })
      .catch((e) => { setErr(e); setItems([]); });
  }, []);

  const tier = session?.license?.tier || session?.tier;
  const expires = session?.license?.expires_at || session?.expires_at;
  const hasCustomer = !!(session?.customer || session?.license?.customer);

  return (
    <div className="max-w-6xl mx-auto px-6 py-10">
      {/* Hero — flat surface with an accent-tinted border. No gradient surfaces
          (cnak/frontend uses flat g-* surfaces only; gradients are reserved for
          text on cnak-landing). */}
      <section className="rounded border border-g-accent-main/30 bg-g-elevated p-6 sm:p-8 mb-8">
        <div className="flex flex-col sm:flex-row sm:items-end sm:justify-between gap-4">
          <div>
            <p className="text-xs uppercase tracking-wider text-g-accent-text font-semibold">{brand.catalogHeroEyebrow}</p>
            <h1 className="mt-1 text-2xl sm:text-3xl font-semibold tracking-tight text-g-text">
              {session?.license?.customer || session?.customer || 'Welcome'}
            </h1>
            <p className="mt-2 text-sm text-g-text-secondary max-w-xl">
              Pull container images, Helm charts and binaries you're entitled to. Use the
              same credential you used to sign in for{' '}
              <code className="px-1 py-0.5 bg-g-secondary rounded text-xs">docker login</code>{' '}
              and friends.
            </p>
          </div>
          <div className="flex flex-col sm:items-end gap-2">
            <div className="flex flex-wrap items-center gap-2 text-xs">
              {tier && <span className="chip-accent">{tier}</span>}
              {expires && (
                <span className="chip">
                  Expires {new Date(expires).toLocaleDateString()}
                </span>
              )}
            </div>
            {hasCustomer && (
              <a
                href={catalog.downloadLicense()}
                download
                className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded text-xs font-medium bg-transparent border border-g-border-medium text-g-text-secondary hover:text-g-text hover:bg-g-hover transition-colors"
              >
                <MdDownload /> Download license
              </a>
            )}
          </div>
        </div>
      </section>

      {/* Packages */}
      <section>
        <div className="flex items-baseline justify-between mb-4">
          <h2 className="text-lg font-semibold">Available packages</h2>
          {items && items.length > 0 && (
            <span className="text-xs text-g-text-secondary">{items.length} package{items.length === 1 ? '' : 's'}</span>
          )}
        </div>

        <ErrorBanner error={err} />

        {items === null ? (
          <div className="py-16"><Spinner /></div>
        ) : items.length === 0 ? (
          <EmptyState
            icon={MdInventory2}
            title="No packages entitled yet"
            description="Your license doesn't grant any packages. Contact your account manager to add entitlements."
          />
        ) : (
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
            {items.map((p) => {
              const Icon = kindIcon[p.kind] || MdInventory2;
              return (
                <Link
                  key={p.id || p.slug}
                  to={`/catalog/p/${encodeURIComponent(p.slug)}`}
                  className="group block bg-g-elevated border border-g-border-weak rounded p-4 hover:border-g-accent-main/50 hover:shadow-z1 transition-all"
                >
                  <div className="flex items-start gap-3">
                    <div className="w-10 h-10 shrink-0 rounded bg-g-accent-main/10 text-g-accent-text flex items-center justify-center">
                      <Icon className="text-xl" />
                    </div>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center justify-between gap-2">
                        <h3 className="font-semibold text-sm text-g-text truncate">{p.display_name || p.slug}</h3>
                        <span className="chip text-[10px]">{kindLabel[p.kind] || p.kind}</span>
                      </div>
                      <p className="mt-1 text-xs text-g-text-secondary line-clamp-3">
                        {p.description || `Pull at ${p.path}`}
                      </p>
                      <div className="mt-3 flex items-center text-xs font-medium text-g-accent-text opacity-0 group-hover:opacity-100 transition-opacity">
                        View install instructions <MdArrowForward className="ml-1" />
                      </div>
                    </div>
                  </div>
                </Link>
              );
            })}
          </div>
        )}
      </section>
    </div>
  );
}
