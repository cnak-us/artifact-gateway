import { createContext, useCallback, useContext, useState } from 'react';
import { createPortal } from 'react-dom';
import { MdCheckCircle, MdError, MdInfo, MdClose } from 'react-icons/md';
import clsx from 'clsx';

const ToastCtx = createContext(null);

const variantCfg = {
  success: { icon: MdCheckCircle, color: 'text-g-green-text', border: 'border-g-green-main/30' },
  error:   { icon: MdError,       color: 'text-g-red-text',   border: 'border-g-red-main/30' },
  info:    { icon: MdInfo,        color: 'text-g-accent-text', border: 'border-g-accent-main/30' },
};

let nextId = 1;

export function ToastProvider({ children }) {
  const [items, setItems] = useState([]);

  const dismiss = useCallback((id) => {
    setItems((xs) => xs.filter((x) => x.id !== id));
  }, []);

  const push = useCallback((variant, message, duration = 4500) => {
    const id = nextId++;
    setItems((xs) => [...xs, { id, variant, message }]);
    if (duration > 0) setTimeout(() => dismiss(id), duration);
    return id;
  }, [dismiss]);

  const value = {
    success: (m, d) => push('success', m, d),
    error:   (m, d) => push('error',   m, d ?? 6000),
    info:    (m, d) => push('info',    m, d),
    dismiss,
  };

  return (
    <ToastCtx.Provider value={value}>
      {children}
      {createPortal(
        <div className="fixed bottom-4 right-4 z-[3000] flex flex-col gap-2 pointer-events-none">
          {items.map((t) => {
            const cfg = variantCfg[t.variant] || variantCfg.info;
            const Icon = cfg.icon;
            return (
              <div
                key={t.id}
                className={clsx(
                  'pointer-events-auto min-w-[280px] max-w-md',
                  'bg-g-elevated border rounded shadow-z2 px-3 py-2.5',
                  'flex items-start gap-2.5 text-sm animate-slideIn',
                  cfg.border,
                )}
                role="status"
              >
                <Icon className={clsx('text-base shrink-0 mt-0.5', cfg.color)} />
                <div className="flex-1 text-g-text min-w-0 break-words">{t.message}</div>
                <button
                  type="button"
                  onClick={() => dismiss(t.id)}
                  className="text-g-text-secondary hover:text-g-text -mr-1 -mt-0.5 p-0.5 rounded"
                  aria-label="Dismiss"
                >
                  <MdClose />
                </button>
              </div>
            );
          })}
        </div>,
        document.body,
      )}
    </ToastCtx.Provider>
  );
}

export function useToast() {
  const ctx = useContext(ToastCtx);
  if (!ctx) throw new Error('useToast must be used within <ToastProvider>');
  return ctx;
}
