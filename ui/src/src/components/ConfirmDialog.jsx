import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react';
import { MdWarning } from 'react-icons/md';
import Modal from './Modal.jsx';
import Button from './Button.jsx';

const ConfirmCtx = createContext(null);

export function ConfirmProvider({ children }) {
  const [state, setState] = useState(null);
  const resolverRef = useRef(null);
  const confirmBtnRef = useRef(null);

  const confirm = useCallback((opts = {}) => {
    return new Promise((resolve) => {
      resolverRef.current = resolve;
      setState({
        title: opts.title || 'Are you sure?',
        message: opts.message || '',
        confirmLabel: opts.confirmLabel || 'Confirm',
        cancelLabel: opts.cancelLabel || 'Cancel',
        danger: opts.danger ?? true,
      });
    });
  }, []);

  const resolve = useCallback((value) => {
    const r = resolverRef.current;
    resolverRef.current = null;
    setState(null);
    r?.(value);
  }, []);

  const onCancel = useCallback(() => resolve(false), [resolve]);
  const onConfirm = useCallback(() => resolve(true), [resolve]);

  useEffect(() => {
    if (!state) return;
    const id = window.setTimeout(() => confirmBtnRef.current?.focus(), 0);
    const onKey = (e) => { if (e.key === 'Enter') { e.preventDefault(); onConfirm(); } };
    document.addEventListener('keydown', onKey);
    return () => { window.clearTimeout(id); document.removeEventListener('keydown', onKey); };
  }, [state, onConfirm]);

  return (
    <ConfirmCtx.Provider value={confirm}>
      {children}
      <Modal
        open={!!state}
        onClose={onCancel}
        size="sm"
        title={state?.title}
        footer={state && (
          <>
            <Button variant="ghost" onClick={onCancel}>{state.cancelLabel}</Button>
            <Button
              ref={confirmBtnRef}
              variant={state.danger ? 'danger' : 'primary'}
              onClick={onConfirm}
            >
              {state.confirmLabel}
            </Button>
          </>
        )}
      >
        {state && (
          <div className="flex gap-3 text-sm text-g-text">
            {state.danger && (
              <MdWarning className="text-xl text-g-red-text shrink-0 mt-0.5" aria-hidden="true" />
            )}
            <div className="min-w-0 whitespace-pre-wrap break-words">{state.message}</div>
          </div>
        )}
      </Modal>
    </ConfirmCtx.Provider>
  );
}

export function useConfirm() {
  const ctx = useContext(ConfirmCtx);
  if (!ctx) throw new Error('useConfirm must be used within <ConfirmProvider>');
  return ctx;
}
