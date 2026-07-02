import {
  type ReactNode,
  type MouseEvent,
  type AnimationEvent,
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
} from 'react';
import { X } from 'lucide-react';
import { cn } from '@/lib/utils';

// Each Drawer generates a unique title id and hands it to its DrawerTitle, so a
// drawer that's closing while something else opens can't collide on a hardcoded
// id. Mirrors Dialog.
const DrawerTitleIdContext = createContext<string | undefined>(undefined);

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

interface DrawerProps {
  open: boolean;
  onClose: () => void;
  children: ReactNode;
  className?: string;
  /** Panel width utility (default: a comfortable inspector width). */
  width?: string;
}

// Right-side slide-over. Same a11y as Dialog (focus-trap, Escape, body-scroll
// lock) but it animates OUT before unmounting — Dialog pops out instantly. The
// component drives enter vs exit by swapping the slide class and unmounts on the
// panel's animationend; OS reduced-motion collapses both to an instant (index.css).
export function Drawer({ open, onClose, children, className, width = 'max-w-md' }: DrawerProps) {
  const [mounted, setMounted] = useState(open);
  const [closing, setClosing] = useState(false);
  const [prevOpen, setPrevOpen] = useState(open);
  const contentRef = useRef<HTMLDivElement>(null);
  const titleId = useId();

  // Derive mount/closing from the open transition during render — React's
  // "adjusting state when a prop changes" pattern — so the exit animation can
  // play after `open` flips false, without a cascading effect.
  if (open !== prevOpen) {
    setPrevOpen(open);
    if (open) {
      setMounted(true);
      setClosing(false);
    } else if (mounted) {
      setClosing(true);
    }
  }

  // Read onClose through a ref so the keydown handler stays stable — otherwise
  // the listeners effect re-runs on every parent render.
  const onCloseRef = useRef(onClose);
  useEffect(() => {
    onCloseRef.current = onClose;
  });

  const handleEscape = useCallback((e: KeyboardEvent) => {
    if (e.key === 'Escape') onCloseRef.current();
  }, []);

  const handleFocusTrap = useCallback((e: KeyboardEvent) => {
    if (e.key !== 'Tab' || !contentRef.current) return;
    const f = contentRef.current.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR);
    if (f.length === 0) return;
    const first = f[0];
    const last = f[f.length - 1];
    if (e.shiftKey) {
      if (document.activeElement === first) {
        e.preventDefault();
        last.focus();
      }
    } else if (document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }, []);

  // Listeners + scroll-lock while mounted. Stable callbacks → set up once per
  // mount, NOT on every keystroke.
  useEffect(() => {
    if (!mounted) return;
    document.addEventListener('keydown', handleEscape);
    document.addEventListener('keydown', handleFocusTrap);
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', handleEscape);
      document.removeEventListener('keydown', handleFocusTrap);
      document.body.style.overflow = '';
    };
  }, [mounted, handleEscape, handleFocusTrap]);

  // Autofocus the first field once, when the panel mounts (opens). Keyed on
  // `mounted` so it fires after the panel renders — and crucially NOT on every
  // render, which would yank focus out of the field you're typing in.
  useEffect(() => {
    if (!mounted) return;
    requestAnimationFrame(() => {
      contentRef.current?.querySelector<HTMLElement>(FOCUSABLE_SELECTOR)?.focus();
    });
  }, [mounted]);

  const onPanelAnimEnd = (e: AnimationEvent) => {
    if (e.target !== e.currentTarget) return; // ignore bubbled child animations
    if (closing) {
      setMounted(false);
      setClosing(false);
    }
  };

  // Safety net: if the exit animationend never arrives — tab backgrounded
  // mid-close, or reduced-motion suppressing the event — force the unmount so
  // the overlay can't get stuck scroll-locked over the page. Longer than the
  // longest exit animation; the real animationend clears it first on the happy
  // path.
  useEffect(() => {
    if (!closing) return;
    const id = setTimeout(() => {
      setMounted(false);
      setClosing(false);
    }, 400);
    return () => clearTimeout(id);
  }, [closing]);

  if (!mounted) return null;

  return (
    <div className="fixed inset-0 z-50" role="dialog" aria-modal="true" aria-labelledby={titleId}>
      <div
        className={cn(
          'fixed inset-0 bg-black/70 backdrop-blur-md',
          closing ? 'animate-overlay-out' : 'animate-overlay-in',
        )}
        aria-hidden="true"
        onClick={onClose}
      />
      <div
        ref={contentRef}
        onAnimationEnd={onPanelAnimEnd}
        onClick={(e: MouseEvent) => e.stopPropagation()}
        className={cn(
          'fixed inset-y-0 right-0 z-50 flex h-full w-full flex-col border-l border-border bg-card/80 backdrop-blur-xl shadow-2xl shadow-black/40',
          width,
          closing ? 'animate-slide-out-right' : 'animate-slide-in-right',
          className,
        )}
      >
        <DrawerTitleIdContext.Provider value={titleId}>{children}</DrawerTitleIdContext.Provider>
      </div>
    </div>
  );
}

export function DrawerHeader({ children, onClose }: { children: ReactNode; onClose?: () => void }) {
  return (
    <div className="flex items-start justify-between gap-4 border-b border-border/60 px-6 py-5">
      <div className="min-w-0">{children}</div>
      {onClose && (
        <button
          onClick={onClose}
          aria-label="Close"
          className="-mr-1.5 -mt-0.5 shrink-0 rounded-[6px] p-1.5 text-muted-foreground transition-colors hover:bg-hover hover:text-foreground"
        >
          <X className="h-4 w-4" />
        </button>
      )}
    </div>
  );
}

export function DrawerTitle({ children, className }: { children: ReactNode; className?: string }) {
  const id = useContext(DrawerTitleIdContext);
  return (
    <h2 id={id} className={cn('text-base font-semibold tracking-tight text-foreground', className)}>
      {children}
    </h2>
  );
}

export function DrawerDescription({ children }: { children: ReactNode }) {
  return <p className="mt-1 text-[12px] text-muted-foreground">{children}</p>;
}

export function DrawerBody({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn('flex-1 overflow-y-auto px-6 py-5', className)}>{children}</div>;
}

export function DrawerFooter({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-center justify-end gap-2 border-t border-border/60 px-6 py-4">
      {children}
    </div>
  );
}
