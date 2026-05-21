import React from 'react';
import cnak from './presets/cnak.jsx';

/** @type {import('./types.js').Brand} */
export const brand = { ...cnak }; // start with the static preset; runtime override mutates in-place

/**
 * Write the active brand's accent colors into --g-accent-main / --g-accent-text on <html>.
 * Call this at boot AFTER the theme class is set, and again any time the theme changes.
 *
 * @param {'light' | 'dark'} mode
 */
export function applyBrandAccent(mode) {
  const a = brand.accent[mode];
  if (!a) return;
  const root = document.documentElement;
  root.style.setProperty('--g-accent-main', a.main);
  root.style.setProperty('--g-accent-text', a.text);
}

// Snake_case -> camelCase field map for the runtime API payload.
const FIELD_MAP = {
  product_name:          'productName',
  vendor:                'vendor',
  vendor_short:          'vendorShort',
  footer_tagline:        'footerTagline',
  embedded_tagline:      'embeddedTagline',
  catalog_hero_eyebrow:  'catalogHeroEyebrow',
  html_title:            'htmlTitle',
  meta_description:      'metaDescription',
};

function buildLogoComponent(svgMarkup) {
  // Wrap raw <svg>…</svg> markup so brand.Logo stays a function matching the
  // preset shape. The admin-supplied SVG is inlined via dangerouslySetInnerHTML;
  // it's trusted (only admins can PUT it) — no V1 sanitizer.
  return function RuntimeLogo({ className = 'w-7 h-7' }) {
    return React.createElement('span', {
      className,
      style: { display: 'inline-flex' },
      dangerouslySetInnerHTML: { __html: svgMarkup },
    });
  };
}

/**
 * Fetch /api/branding and merge non-empty fields over the CNAK preset.
 * Updates strings, accent CSS vars (re-applied for the current theme), and
 * document.title. Replaces brand.Logo if logo_svg is provided.
 *
 * Idempotent and silent on failure — if the endpoint is down or returns junk,
 * the static CNAK preset stays in effect.
 *
 * @returns {Promise<void>}
 */
export async function loadRuntimeBrand() {
  let payload;
  try {
    const res = await fetch('/api/branding', { credentials: 'same-origin' });
    if (!res.ok) return;
    payload = await res.json();
  } catch {
    return; // network/parse error — keep preset
  }
  if (!payload || typeof payload !== 'object') return;

  // String fields
  for (const [snake, camel] of Object.entries(FIELD_MAP)) {
    const v = payload[snake];
    if (typeof v === 'string' && v.trim() !== '') brand[camel] = v;
  }

  // Accent colors — preserve preset values when payload field is empty.
  const a = brand.accent;
  if (typeof payload.accent_light_main === 'string' && payload.accent_light_main) a.light.main = payload.accent_light_main;
  if (typeof payload.accent_light_text === 'string' && payload.accent_light_text) a.light.text = payload.accent_light_text;
  if (typeof payload.accent_dark_main  === 'string' && payload.accent_dark_main)  a.dark.main  = payload.accent_dark_main;
  if (typeof payload.accent_dark_text  === 'string' && payload.accent_dark_text)  a.dark.text  = payload.accent_dark_text;

  // Logo override (raw SVG markup)
  if (typeof payload.logo_svg === 'string' && payload.logo_svg.trim().startsWith('<svg')) {
    brand.Logo = buildLogoComponent(payload.logo_svg);
  }

  // Re-apply visible side effects with the merged values.
  document.title = brand.htmlTitle;
  const mode = document.documentElement.classList.contains('dark') ? 'dark' : 'light';
  applyBrandAccent(mode);

  // Fire an event so React components subscribed to brand updates can re-render.
  window.dispatchEvent(new CustomEvent('cnak:brand', { detail: { source: 'runtime' } }));
}
