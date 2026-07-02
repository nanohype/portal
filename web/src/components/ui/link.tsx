import type { AnchorHTMLAttributes } from 'react';
import { navigate } from '@/hooks/useNavigate';

interface LinkProps extends AnchorHTMLAttributes<HTMLAnchorElement> {
  href: string;
}

export function Link({ href, onClick, children, ...props }: LinkProps) {
  const handleClick = (e: React.MouseEvent<HTMLAnchorElement>) => {
    // Let modified clicks (cmd+click, ctrl+click) and external links pass through
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
    if (href.startsWith('http') || href.startsWith('//')) return;

    e.preventDefault();
    navigate(href);
    onClick?.(e);
  };

  return (
    <a href={href} onClick={handleClick} {...props}>
      {children}
    </a>
  );
}
