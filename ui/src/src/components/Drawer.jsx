import { useEffect } from 'react';
import { createPortal } from 'react-dom';
import { MdClose } from 'react-icons/md';
import IconButton from './IconButton.jsx';

export default function Drawer({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  width = 'w-[520px]',
}) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e) => { if (e.key === 'Escape') onClose?.(); };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [open, onClose]);

  if (!open) return null;

  return createPortal(
    <div className="fixed inset-0 z-[2000] flex">
      <div
        className="flex-1 bg-black/40 animate-fadeIn"
        onClick={onClose}
        aria-hidden="true"
      />
      <aside
        className={`${width} max-w-full bg-g-elevated border-l border-g-border-weak shadow-z3 flex flex-col animate-slideIn`}
        role="dialog"
        aria-modal="true"
      >
        <div className="flex items-start justify-between gap-4 px-5 pt-4 pb-3 border-b border-g-border-weak">
          <div className="min-w-0">
            {title && <h2 className="text-base font-semibold text-g-text truncate">{title}</h2>}
            {description && <p className="mt-1 text-xs text-g-text-secondary">{description}</p>}
          </div>
          <IconButton icon={<MdClose className="text-lg" />} label="Close" onClick={onClose} />
        </div>
        <div className="flex-1 overflow-y-auto px-5 py-4">{children}</div>
        {footer && (
          <div className="px-5 py-3 border-t border-g-border-weak bg-g-secondary/50 flex items-center justify-end gap-2">
            {footer}
          </div>
        )}
      </aside>
    </div>,
    document.body,
  );
}
