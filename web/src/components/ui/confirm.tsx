import { useCallback, useRef, useState, type ReactNode } from 'react';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from './dialog';
import { Button } from './button';
import { Input } from './input';
import { ConfirmContext, type ConfirmFn, type ConfirmOptions } from './confirm-context';

// ConfirmProvider renders one shared confirm Dialog and hands descendants a
// promise-based `confirm()` via useConfirm — a themeable, focus-trapped,
// reduced-motion-aware replacement for the native window.confirm().
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [opts, setOpts] = useState<ConfirmOptions | null>(null);
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState('');
  const resolveRef = useRef<((value: boolean) => void) | null>(null);

  const confirm = useCallback<ConfirmFn>(
    (options) =>
      new Promise<boolean>((resolve) => {
        resolveRef.current = resolve;
        setTyped(''); // reset any prior type-to-confirm entry
        setOpts(options);
        setOpen(true);
      }),
    [],
  );

  // Resolve the pending promise and close. opts is retained so the dialog
  // content stays put through the exit animation.
  const settle = useCallback((result: boolean) => {
    resolveRef.current?.(result);
    resolveRef.current = null;
    setOpen(false);
  }, []);

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      <Dialog open={open} onClose={() => settle(false)}>
        {opts && (
          <DialogContent>
            <DialogHeader>
              <DialogTitle>{opts.title}</DialogTitle>
              {opts.message && <DialogDescription>{opts.message}</DialogDescription>}
            </DialogHeader>
            {opts.requireText && (
              <div className="mt-4">
                <label className="text-[11px] text-muted-foreground mb-1.5 block">
                  Type <span className="font-mono text-foreground">{opts.requireText}</span> to
                  confirm
                </label>
                <Input
                  autoFocus
                  value={typed}
                  onChange={(e) => setTyped(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && typed === opts.requireText) settle(true);
                  }}
                  className="font-mono"
                />
              </div>
            )}
            <div className="flex justify-end gap-2 mt-6">
              <Button variant="outline" size="sm" onClick={() => settle(false)}>
                {opts.cancelLabel ?? 'Cancel'}
              </Button>
              <Button
                variant={opts.destructive === false ? 'default' : 'destructive'}
                size="sm"
                disabled={!!opts.requireText && typed !== opts.requireText}
                onClick={() => settle(true)}
              >
                {opts.confirmLabel ?? 'Confirm'}
              </Button>
            </div>
          </DialogContent>
        )}
      </Dialog>
    </ConfirmContext.Provider>
  );
}
