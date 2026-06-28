import { createContext, useContext } from "react";

export interface ConfirmOptions {
  title: string;
  message?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  /** Style the confirm button as destructive (red). Defaults to true — most
   *  confirms guard a delete/destroy. Pass false for benign confirmations. */
  destructive?: boolean;
  /** Require the user to type this exact string before confirm enables. For
   *  break-glass actions (e.g. unwedge) where a misclick must be impossible —
   *  pass the resource name so deleting it can't be muscle-memory. */
  requireText?: string;
}

export type ConfirmFn = (opts: ConfirmOptions) => Promise<boolean>;

export const ConfirmContext = createContext<ConfirmFn | null>(null);

// useConfirm returns an async confirm(). Awaiting it resolves true on confirm,
// false on cancel/escape/overlay-click.
export function useConfirm(): ConfirmFn {
  const ctx = useContext(ConfirmContext);
  if (!ctx) {
    throw new Error("useConfirm must be used within a ConfirmProvider");
  }
  return ctx;
}
