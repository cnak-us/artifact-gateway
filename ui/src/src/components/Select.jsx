// Themed dropdown that mirrors the Input visual style. The trigger looks like
// an Input, and the open listbox is rendered through a portal so it isn't
// clipped by Modals or Drawers and never falls back to OS-native chrome.
//
// The onChange contract matches the previous native-<select> wrapper: it
// fires with a synthetic event shape ({ target: { name, value } }) so all
// existing `e.target.value` handlers keep working.
import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { createPortal } from 'react-dom';
import clsx from 'clsx';
import { MdExpandMore, MdCheck } from 'react-icons/md';

function fireChange(onChange, name, value) {
  if (!onChange) return;
  const target = { name: name || '', value };
  onChange({ target, currentTarget: target });
}

const Select = forwardRef(function Select(
  {
    label,
    error,
    hint,
    options = [],
    placeholder,
    className = '',
    id,
    value,
    onChange,
    disabled,
    name,
    ...rest
  },
  ref,
) {
  const reactId = useId();
  const inputId = id || name || reactId;
  const errorId = error ? `${inputId}-error` : undefined;
  const listboxId = `${inputId}-listbox`;

  const buttonRef = useRef(null);
  const menuRef = useRef(null);
  useImperativeHandle(ref, () => buttonRef.current, []);

  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState(null);
  const [active, setActive] = useState(-1);

  const selectedIdx = useMemo(
    () => options.findIndex((o) => String(o.value) === String(value ?? '')),
    [options, value],
  );
  const selected = selectedIdx >= 0 ? options[selectedIdx] : null;

  const reposition = useCallback(() => {
    const btn = buttonRef.current;
    if (!btn) return;
    const rect = btn.getBoundingClientRect();
    const vh = window.innerHeight;
    const margin = 4;
    const desiredMax = 280;
    const spaceBelow = vh - rect.bottom;
    const spaceAbove = rect.top;
    const placeAbove = spaceBelow < 200 && spaceAbove > spaceBelow;
    const maxHeight = Math.max(
      120,
      Math.min(desiredMax, (placeAbove ? spaceAbove : spaceBelow) - margin - 8),
    );
    setPos({
      left: rect.left,
      width: rect.width,
      anchorTop: rect.top,
      anchorBottom: rect.bottom,
      maxHeight,
      placement: placeAbove ? 'top' : 'bottom',
    });
  }, []);

  useLayoutEffect(() => {
    if (!open) return undefined;
    reposition();
    const onScroll = () => reposition();
    window.addEventListener('scroll', onScroll, true);
    window.addEventListener('resize', reposition);
    return () => {
      window.removeEventListener('scroll', onScroll, true);
      window.removeEventListener('resize', reposition);
    };
  }, [open, reposition]);

  useEffect(() => {
    if (!open) return undefined;
    const onDocMouseDown = (e) => {
      if (buttonRef.current?.contains(e.target)) return;
      if (menuRef.current?.contains(e.target)) return;
      setOpen(false);
    };
    const onKey = (e) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        setOpen(false);
        buttonRef.current?.focus();
      }
    };
    document.addEventListener('mousedown', onDocMouseDown);
    document.addEventListener('keydown', onKey, true);
    return () => {
      document.removeEventListener('mousedown', onDocMouseDown);
      document.removeEventListener('keydown', onKey, true);
    };
  }, [open]);

  useEffect(() => {
    if (!open) return;
    setActive(selectedIdx >= 0 ? selectedIdx : 0);
  }, [open, selectedIdx]);

  useEffect(() => {
    if (!open || active < 0 || !menuRef.current) return;
    const el = menuRef.current.querySelector(`[data-idx="${active}"]`);
    el?.scrollIntoView({ block: 'nearest' });
  }, [open, active]);

  const choose = (idx) => {
    const opt = options[idx];
    if (!opt || opt.disabled) return;
    fireChange(onChange, name, opt.value);
    setOpen(false);
    buttonRef.current?.focus();
  };

  const onTriggerKeyDown = (e) => {
    if (disabled) return;
    if (!open) {
      if (
        e.key === 'ArrowDown'
        || e.key === 'ArrowUp'
        || e.key === 'Enter'
        || e.key === ' '
      ) {
        e.preventDefault();
        setOpen(true);
      }
      return;
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setActive((i) => Math.min(options.length - 1, (i < 0 ? -1 : i) + 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setActive((i) => Math.max(0, (i < 0 ? options.length : i) - 1));
    } else if (e.key === 'Home') {
      e.preventDefault();
      setActive(0);
    } else if (e.key === 'End') {
      e.preventDefault();
      setActive(options.length - 1);
    } else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      if (active >= 0) choose(active);
    } else if (e.key === 'Tab') {
      setOpen(false);
    }
  };

  const triggerLabel = selected ? selected.label : (placeholder || '');

  return (
    <div className={clsx('w-full', className)}>
      {label && (
        <label
          htmlFor={inputId}
          className="block text-xs font-medium text-g-text-secondary mb-1.5"
        >
          {label}
        </label>
      )}
      <div className="relative">
        <button
          ref={buttonRef}
          type="button"
          id={inputId}
          name={name}
          disabled={disabled}
          aria-haspopup="listbox"
          aria-expanded={open}
          aria-controls={open ? listboxId : undefined}
          aria-invalid={!!error || undefined}
          aria-describedby={errorId}
          onClick={() => { if (!disabled) setOpen((o) => !o); }}
          onKeyDown={onTriggerKeyDown}
          className={clsx(
            'w-full inline-flex items-center justify-between gap-2',
            'bg-g-secondary border rounded pl-3 pr-9 py-2 text-sm text-left',
            'focus:border-g-accent-main focus:outline-none focus:ring-2 focus:ring-g-accent-main/40',
            'disabled:opacity-50 disabled:cursor-not-allowed',
            error ? 'border-g-red-main' : 'border-g-border-medium',
            selected ? 'text-g-text' : 'text-g-text-disabled',
          )}
          {...rest}
        >
          <span className="truncate">{triggerLabel || ' '}</span>
          <MdExpandMore
            className={clsx(
              'pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2 text-g-text-secondary transition-transform',
              open && 'rotate-180',
            )}
          />
        </button>
      </div>
      {error && (
        <p id={errorId} className="mt-1 text-xs text-g-red-text">{error}</p>
      )}
      {!error && hint && (
        <p className="mt-1 text-xs text-g-text-disabled">{hint}</p>
      )}

      {open && pos && createPortal(
        <ul
          ref={menuRef}
          id={listboxId}
          role="listbox"
          aria-labelledby={inputId}
          tabIndex={-1}
          style={{
            position: 'fixed',
            left: pos.left,
            width: pos.width,
            maxHeight: pos.maxHeight,
            ...(pos.placement === 'top'
              ? { bottom: window.innerHeight - pos.anchorTop + 4 }
              : { top: pos.anchorBottom + 4 }),
          }}
          className="z-[2100] overflow-y-auto bg-g-elevated border border-g-border-weak rounded shadow-z3 py-1 animate-fadeIn"
        >
          {options.length === 0 ? (
            <li className="px-3 py-2 text-xs text-g-text-disabled">
              No options
            </li>
          ) : options.map((o, i) => {
            const isSelected = i === selectedIdx;
            const isActive = i === active;
            const optDisabled = !!o.disabled;
            return (
              <li
                key={o.value ?? i}
                role="option"
                aria-selected={isSelected}
                aria-disabled={optDisabled || undefined}
                data-idx={i}
                onMouseEnter={() => !optDisabled && setActive(i)}
                onMouseDown={(e) => {
                  e.preventDefault();
                  if (!optDisabled) choose(i);
                }}
                className={clsx(
                  'flex items-center gap-2 px-3 py-1.5 text-sm select-none',
                  optDisabled
                    ? 'text-g-text-disabled cursor-not-allowed'
                    : 'cursor-pointer',
                  !optDisabled && isActive && 'bg-g-hover',
                  isSelected
                    ? 'text-g-accent-text font-medium'
                    : !optDisabled && 'text-g-text',
                )}
              >
                <span className="flex-1 truncate">{o.label}</span>
                {isSelected && (
                  <MdCheck className="text-g-accent-text shrink-0" />
                )}
              </li>
            );
          })}
        </ul>,
        document.body,
      )}
    </div>
  );
});

export default Select;
