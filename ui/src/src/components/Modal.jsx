import { useEffect, useRef } from 'react';
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

// focusableSelector lists every element that can receive Tab focus. Kept in
// sync with the WCAG-recommended "what's focusable" list; excludes elements
// with [tabindex=-1] (programmatic-focus only) and disabled controls.
const focusableSelector = [
  'a[href]',
  'area[href]',
  'input:not([disabled]):not([type=hidden])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  'button:not([disabled])',
  'iframe',
  '[tabindex]:not([tabindex="-1"])',
  '[contenteditable=true]',
].join(',');

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
  const dialogRef = useRef(null);
  const restoreRef = useRef(null);

  useEffect(() => {
    if (!open) return;
    // Save focus so we can restore it when the modal closes — meets WCAG's
    // "return focus to the invoking element" requirement.
    restoreRef.current = typeof document !== 'undefined' ? document.activeElement : null;

    const onKey = (e) => {
      if (e.key === 'Escape') {
        onClose?.();
        return;
      }
      if (e.key !== 'Tab') return;
      const node = dialogRef.current;
      if (!node) return;
      const items = node.querySelectorAll(focusableSelector);
      if (!items.length) {
        e.preventDefault();
        return;
      }
      const first = items[0];
      const last = items[items.length - 1];
      if (e.shiftKey) {
        if (document.activeElement === first || !node.contains(document.activeElement)) {
          e.preventDefault();
          last.focus();
        }
      } else {
        if (document.activeElement === last) {
          e.preventDefault();
          first.focus();
        }
      }
    };
    document.addEventListener('keydown', onKey);

    // Initial focus — first focusable element inside the dialog so screen
    // readers and keyboard users land in the modal, not in the page behind.
    const t = window.setTimeout(() => {
      const node = dialogRef.current;
      if (!node) return;
      const first = node.querySelector(focusableSelector);
      (first || node).focus?.();
    }, 0);

    return () => {
      document.removeEventListener('keydown', onKey);
      window.clearTimeout(t);
      // Restore focus to the element that opened the modal.
      const prev = restoreRef.current;
      restoreRef.current = null;
      if (prev && typeof prev.focus === 'function') {
        try { prev.focus(); } catch { /* removed from DOM */ }
      }
    };
  }, [open, onClose]);

  if (!open) return null;

  return createPortal(
    <div className="fixed inset-0 z-[2000] flex items-center justify-center p-4 bg-black/60 animate-fadeIn">
      <div
        ref={dialogRef}
        tabIndex={-1}
        className={clsx(
          'w-full max-h-[90vh] flex flex-col bg-g-elevated border border-g-border-weak rounded shadow-z3 outline-none',
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
