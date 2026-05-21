import React from 'react';
import ReactDOM from 'react-dom/client';
import App from './App.jsx';
import { brand, applyBrandAccent, loadRuntimeBrand } from './brand/index.js';
import './index.css';

// Title is set at runtime so the brand module is the single source of truth;
// index.html's static <title> is just a pre-paint fallback.
document.title = brand.htmlTitle;

// Theme bootstrap. Mirrors cnak/frontend's ThemeContext default:
//   1. Honor the user's persisted choice from localStorage if present.
//   2. Otherwise honor the OS `prefers-color-scheme`.
// We apply the class to <html> synchronously *before* React mounts so there
// is no light/dark flash on first paint.
(function bootstrapTheme() {
  try {
    const stored = localStorage.getItem(brand.themeStorageKey); // 'light' | 'dark'
    const prefersDark = window.matchMedia?.('(prefers-color-scheme: dark)').matches;
    let mode = stored || (prefersDark ? 'dark' : 'light');
    if (mode === 'low-light') mode = 'dark';
    const root = document.documentElement;
    root.classList.remove('dark');
    if (mode === 'dark') root.classList.add(mode);
    applyBrandAccent(mode);
  } catch {
    // localStorage / matchMedia blocked — fall through with default (light).
  }
})();

// Re-apply brand accent when ThemeContext switches themes.
window.addEventListener('cnak:theme', (e) => applyBrandAccent(e.detail));

// Hydrate runtime brand overrides from the server. Fire-and-forget — the
// static CNAK preset already painted; this swaps in any persisted overrides.
loadRuntimeBrand();

ReactDOM.createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
