import { createContext, useContext, useEffect, useState } from 'react';
import { MdLightMode, MdDarkMode } from 'react-icons/md';
import { brand } from '../brand/index.js';

const STORAGE_KEY = brand.themeStorageKey;

export const THEMES = [
  { id: 'light',     label: 'Light',     icon: MdLightMode },
  { id: 'dark',      label: 'Dark',      icon: MdDarkMode },
];

const ThemeContext = createContext(null);

function readActiveTheme() {
  // The pre-paint bootstrap in index.html has already set the class. Trust it.
  const c = document.documentElement.classList;
  if (c.contains('dark'))      return 'dark';
  return 'light';
}

export function ThemeProvider({ children }) {
  const [theme, setThemeState] = useState(readActiveTheme);

  const setTheme = (next) => {
    if (!THEMES.some((t) => t.id === next)) return;
    const root = document.documentElement;
    root.classList.remove('dark');
    if (next === 'dark') root.classList.add(next);
    try { localStorage.setItem(STORAGE_KEY, next); } catch { /* storage blocked */ }
    setThemeState(next);
    // Fire a cross-cutting event so the brand-accent applier (and anything
    // else outside React) can react without subscribing to context.
    window.dispatchEvent(new CustomEvent('cnak:theme', { detail: next }));
  };

  // Re-sync if a different tab changes the theme.
  useEffect(() => {
    const onStorage = (e) => {
      if (e.key === STORAGE_KEY && e.newValue && e.newValue !== theme) setTheme(e.newValue);
    };
    window.addEventListener('storage', onStorage);
    return () => window.removeEventListener('storage', onStorage);
  }, [theme]);

  return (
    <ThemeContext.Provider value={{ theme, setTheme, themes: THEMES }}>
      {children}
    </ThemeContext.Provider>
  );
}

export function useTheme() {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error('useTheme must be used inside <ThemeProvider>');
  return ctx;
}
