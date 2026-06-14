import {
  type ReactNode,
  type MouseEvent,
  type AnimationEvent,
  createContext,
  useContext,
  useEffect,
  useCallback,
  useId,
  useRef,
  useState,
} from "react";
import { cn } from "@/lib/utils";

interface DialogProps {
  open: boolean;
  onClose: () => void;
  children: ReactNode;
}

// Each Dialog generates a unique title id and hands it to its DialogTitle, so
// two dialogs in the DOM at once (one closing while another opens) can't collide
// on a hardcoded id and mislabel each other.
const DialogTitleIdContext = createContext<string | undefined>(undefined);

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

export function Dialog({ open, onClose, children }: DialogProps) {
  const contentRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const [mounted, setMounted] = useState(open);
  const [closing, setClosing] = useState(false);

  useEffect(() => {
    if (open) {
      setMounted(true);
      setClosing(false);
    } else if (mounted) {
      setClosing(true); // play the exit, then unmount on animationend
    }
  }, [open, mounted]);

  // Read onClose through a ref so the keydown handler is stable and the listeners
  // effect doesn't re-subscribe (or steal focus) on every parent render.
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const handleEscape = useCallback((e: KeyboardEvent) => {
    if (e.key === "Escape") onCloseRef.current();
  }, []);

  const handleFocusTrap = useCallback((e: KeyboardEvent) => {
    if (e.key !== "Tab" || !contentRef.current) return;
    const focusable =
      contentRef.current.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR);
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
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

  useEffect(() => {
    if (!mounted) return;
    document.addEventListener("keydown", handleEscape);
    document.addEventListener("keydown", handleFocusTrap);
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", handleEscape);
      document.removeEventListener("keydown", handleFocusTrap);
      document.body.style.overflow = "";
    };
  }, [mounted, handleEscape, handleFocusTrap]);

  // Autofocus the first field once when the dialog mounts — not on every render
  // (which would yank focus out of whatever you're typing in).
  useEffect(() => {
    if (!mounted) return;
    requestAnimationFrame(() => {
      contentRef.current?.querySelector<HTMLElement>(FOCUSABLE_SELECTOR)?.focus();
    });
  }, [mounted]);

  // Unmount after the EXIT animation — but only the content's own animationend,
  // not a child's bubbling up (and not the enter animation).
  const onContentAnimEnd = (e: AnimationEvent) => {
    if (e.target !== e.currentTarget) return;
    if (closing) {
      setMounted(false);
      setClosing(false);
    }
  };

  // Safety net: if that exit animationend never arrives — tab backgrounded
  // mid-close, or reduced-motion suppressing the event — force the unmount so
  // the overlay can't get stuck scroll-locked over the page.
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
    <div
      className="fixed inset-0 z-50 flex items-center justify-center"
      role="dialog"
      aria-modal="true"
      aria-labelledby={titleId}
    >
      <div
        className={cn(
          "fixed inset-0 bg-black/70 backdrop-blur-md",
          closing ? "animate-overlay-out" : "animate-overlay-in",
        )}
        aria-hidden="true"
        onClick={onClose}
      />
      <div
        ref={contentRef}
        onAnimationEnd={onContentAnimEnd}
        className={cn(
          "relative z-50 w-full max-w-lg mx-4",
          closing ? "animate-pop-out" : "animate-fade-in",
        )}
      >
        <DialogTitleIdContext.Provider value={titleId}>
          {children}
        </DialogTitleIdContext.Provider>
      </div>
    </div>
  );
}

export function DialogContent({
  className,
  children,
  ...props
}: {
  className?: string;
  children: ReactNode;
}) {
  return (
    <div
      className={cn(
        "rounded-[10px] border border-border bg-card/80 backdrop-blur-xl p-6 shadow-2xl shadow-black/40",
        className
      )}
      onClick={(e: MouseEvent) => e.stopPropagation()}
      {...props}
    >
      {children}
    </div>
  );
}

export function DialogHeader({ children }: { children: ReactNode }) {
  return <div className="mb-5">{children}</div>;
}

export function DialogTitle({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  const id = useContext(DialogTitleIdContext);
  return (
    <h2
      id={id}
      className={cn("text-lg font-semibold text-foreground", className)}
    >
      {children}
    </h2>
  );
}

export function DialogDescription({
  children,
}: {
  children: ReactNode;
}) {
  return <p className="text-sm text-muted-foreground mt-1.5">{children}</p>;
}
