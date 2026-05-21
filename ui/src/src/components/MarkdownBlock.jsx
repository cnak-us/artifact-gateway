import { useMemo } from 'react';
import { marked } from 'marked';

// marked is synchronous and safe-ish for trusted admin-authored content.
// Server should sanitize on write if needed; we don't dangerouslySetInnerHTML
// other user-supplied data anywhere else.
marked.setOptions({ gfm: true, breaks: false });

export default function MarkdownBlock({ source = '', className = '' }) {
  const html = useMemo(() => marked.parse(source || ''), [source]);
  return (
    <div
      className={`markdown ${className}`}
      // eslint-disable-next-line react/no-danger
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}
