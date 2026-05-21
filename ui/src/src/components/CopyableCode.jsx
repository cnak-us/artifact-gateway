import { useState } from 'react';
import { MdContentCopy, MdCheck } from 'react-icons/md';
import clsx from 'clsx';

// Block of monospace text with a "copy to clipboard" button in the corner.
// Used for install commands and one-time credentials.
export default function CopyableCode({
  value,
  language,         // label only (e.g. "shell")
  multiline = true,
  className = '',
  ariaLabel = 'Copy',
}) {
  const [copied, setCopied] = useState(false);

  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {/* clipboard blocked */}
  };

  return (
    <div className={clsx('relative group border border-g-border-weak rounded bg-g-canvas overflow-hidden', className)}>
      {language && (
        <div className="flex items-center justify-between px-3 py-1.5 border-b border-g-border-weak bg-g-secondary/60">
          <span className="text-[11px] uppercase tracking-wider font-medium text-g-text-disabled">{language}</span>
        </div>
      )}
      <button
        type="button"
        onClick={onCopy}
        aria-label={ariaLabel}
        title={copied ? 'Copied!' : 'Copy'}
        className={clsx(
          'absolute top-1.5 right-1.5 z-10',
          'inline-flex items-center gap-1 px-2 py-1 rounded text-[11px] font-medium',
          'bg-g-elevated/80 backdrop-blur border border-g-border-weak text-g-text-secondary',
          'opacity-0 group-hover:opacity-100 focus:opacity-100 transition-opacity',
          copied && 'opacity-100 text-g-green-text border-g-green-main/30',
          language && 'top-9',
        )}
      >
        {copied ? <MdCheck /> : <MdContentCopy />}
        {copied ? 'Copied' : 'Copy'}
      </button>
      <pre className={clsx(
        'm-0 p-3 text-xs text-g-text font-mono leading-relaxed overflow-x-auto',
        multiline ? 'whitespace-pre' : 'whitespace-pre overflow-x-auto',
      )}>{value}</pre>
    </div>
  );
}
