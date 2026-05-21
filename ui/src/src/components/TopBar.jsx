import { Link } from 'react-router-dom';
import Logo from './Logo.jsx';
import { brand as activeBrand } from '../brand/index.js';

const AREA_META = {
  admin:   { subtitle: 'Admin console',    home: '/admin' },
  catalog: { subtitle: 'Customer catalog', home: '/catalog' },
};

// Shared chrome used by both /admin/* and /catalog/* surfaces (and their
// matching login screens). Keeps height, padding, border, background and
// the brand block visually identical across the two areas; only `area`
// (subtitle text) and `rightSlot` (area-specific controls) vary.
export default function TopBar({
  area = 'catalog',
  subtitle,
  rightSlot,
  homeHref,
  linkBrand = true,
}) {
  const meta = AREA_META[area] || AREA_META.catalog;
  const sub = subtitle ?? meta.subtitle;
  const href = homeHref ?? meta.home;

  const brand = (
    <>
      <span className="text-g-accent-text"><Logo className="w-7 h-7" /></span>
      <div>
        <div className="text-sm font-semibold leading-tight text-g-text">{activeBrand.vendor}</div>
        <div className="text-[11px] uppercase tracking-wider text-g-text-secondary leading-tight">
          {sub}
        </div>
      </div>
    </>
  );

  return (
    <header className="sticky top-0 z-40 px-6 h-14 flex items-center justify-between border-b border-g-border-weak bg-g-elevated/85 backdrop-blur">
      {linkBrand ? (
        <Link
          to={href}
          className="flex items-center gap-2.5 hover:text-g-accent-text transition-colors"
        >
          {brand}
        </Link>
      ) : (
        <div className="flex items-center gap-2.5">{brand}</div>
      )}

      {rightSlot ? (
        <div className="flex items-center gap-1 text-sm">{rightSlot}</div>
      ) : null}
    </header>
  );
}
