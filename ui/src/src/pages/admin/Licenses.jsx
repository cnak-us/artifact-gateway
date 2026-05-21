import { useSearchParams } from 'react-router-dom';
import clsx from 'clsx';
import LicensesPanel from './LicensesPanel.jsx';
import RootKeysPanel from './RootKeysPanel.jsx';

// Tab keys are URL-driven so the browser back button works and admins can
// deep-link "Licenses → Root keys" via /admin/licenses?tab=root-keys.
const TABS = [
  { key: 'licenses',  label: 'Licenses' },
  { key: 'root-keys', label: 'Root keys' },
];

export default function Licenses() {
  const [params, setParams] = useSearchParams();
  const requested = params.get('tab');
  const tab = TABS.some((t) => t.key === requested) ? requested : 'licenses';

  const switchTo = (key) => {
    const next = new URLSearchParams(params);
    if (key === 'licenses') next.delete('tab');
    else next.set('tab', key);
    setParams(next, { replace: true });
  };

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-xl font-semibold">Licenses</h1>
        <p className="text-sm text-g-text-secondary max-w-2xl">
          Customer entitlements and the signing keys that issue them.
        </p>
      </div>

      <div role="tablist" className="border-b border-g-border-weak flex gap-4">
        {TABS.map((t) => (
          <button
            key={t.key}
            role="tab"
            aria-selected={tab === t.key}
            onClick={() => switchTo(t.key)}
            className={clsx(
              'px-1 py-2 text-sm font-medium -mb-px border-b-2 transition-colors',
              tab === t.key
                ? 'border-g-accent-main text-g-text'
                : 'border-transparent text-g-text-secondary hover:text-g-text',
            )}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === 'licenses' ? <LicensesPanel /> : <RootKeysPanel />}
    </div>
  );
}
