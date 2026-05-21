import { MdWarning } from 'react-icons/md';

export default function ErrorBanner({ error, className = '' }) {
  if (!error) return null;
  const msg = typeof error === 'string' ? error : (error.message || 'Something went wrong');
  return (
    <div className={`flex items-start gap-2 px-3 py-2 border rounded text-sm bg-g-red-main/10 border-g-red-main/30 text-g-red-text ${className}`}>
      <MdWarning className="mt-0.5 shrink-0" />
      <div className="flex-1">{msg}</div>
    </div>
  );
}
