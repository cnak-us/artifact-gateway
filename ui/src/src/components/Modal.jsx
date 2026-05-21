import { useEffect } from 'react';
import { createPortal } from 'react-dom';
import { MdClose } from 'react-icons/md';
import clsx from 'clsx';
import IconButton from './IconButton.jsx';

const sizeMap = {
  sm: 'max-w-sm',
  md: 'max-w-xl',
  lg: 'max-w-3xl',
  xl: 'max-w-5xl',
};

export default function Modal({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  size = 'md',
  className = '',
}) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e) => { if (e.key === 'Escape') onClose?.(); };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [open, onClose]);

  if (!open) return null;

  return createPortal(
    <div className="fixed inset-0 z-[2000] flex items-center justify-center p-4 bg-black/60 animate-fadeIn">
      <div
        className={clsx(
          'w-full max-h-[90vh] flex flex-col bg-g-elevated border border-g-border-weak rounded shadow-z3',
          sizeMap[size] || sizeMap.md,
          className,
        )}
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
      </div>
    </div>,
    document.body,
  );
}
