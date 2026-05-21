import { useEffect, useRef, useState } from 'react';
import { useTheme } from '../contexts/ThemeContext.jsx';

export default function ThemeToggle() {
  const { theme, setTheme, themes } = useTheme();
  const [open, setOpen] = useState(false);
  const wrapRef = useRef(null);

  useEffect(() => {
    if (!open) return;
    const onClick = (e) => { if (!wrapRef.current?.contains(e.target)) setOpen(false); };
    const onKey   = (e) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', onClick);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onClick);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  const Current = themes.find((t) => t.id === theme)?.icon;

  return (
    <div ref={wrapRef} className="relative">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-label="Theme"
        aria-haspopup="menu"
        aria-expanded={open}
        className="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded font-medium text-sm text-g-text-secondary hover:text-g-text hover:bg-g-hover transition-colors"
      >
        {Current ? <Current className="text-base" /> : null}
      </button>
      {open ? (
        <div
          role="menu"
          className="absolute right-0 top-full mt-1 min-w-[10rem] bg-g-elevated border border-g-border-weak rounded shadow-z2 py-1 z-50"
        >
          {themes.map((t) => {
            const Icon = t.icon;
            const active = t.id === theme;
            return (
              <button
                key={t.id}
                role="menuitemradio"
                aria-checked={active}
                onClick={() => { setTheme(t.id); setOpen(false); }}
                className={`w-full flex items-center gap-2 px-3 py-1.5 text-sm text-left transition-colors ${
                  active
                    ? 'bg-g-accent-main/15 text-g-accent-text'
                    : 'text-g-text hover:bg-g-hover'
                }`}
              >
                <Icon className="text-base" />
                {t.label}
              </button>
            );
          })}
        </div>
      ) : null}
    </div>
  );
}
