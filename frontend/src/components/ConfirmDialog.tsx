import { useState } from "react";
import * as AlertDialog from "@radix-ui/react-alert-dialog";

interface ConfirmDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  confirmLabel?: string;
  confirmVariant?: "danger" | "primary";
  onConfirm: () => void;
  /** If set, user must type this string to confirm */
  confirmText?: string;
}

export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel = "Confirm",
  confirmVariant = "danger",
  onConfirm,
  confirmText,
}: ConfirmDialogProps) {
  const [typed, setTyped] = useState("");
  const canConfirm = confirmText ? typed === confirmText : true;

  return (
    <AlertDialog.Root open={open} onOpenChange={onOpenChange}>
      <AlertDialog.Portal>
        <AlertDialog.Overlay className="fixed inset-0 bg-black/65 backdrop-blur-sm z-[300]" />
        <AlertDialog.Content className="fixed top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 bg-card border border-border rounded-[var(--radius-lg)] p-6 w-full max-w-[400px] shadow-modal z-[301] animate-modal-in">
          <AlertDialog.Title className="text-[17px] font-semibold tracking-tight mb-2">
            {title}
          </AlertDialog.Title>
          <AlertDialog.Description className="text-[13px] text-text-secondary mb-4">
            {description}
          </AlertDialog.Description>
          {confirmText && (
            <div className="mb-4">
              <label className="block text-[11px] font-medium text-text-tertiary uppercase tracking-wider mb-1">
                Type &quot;{confirmText}&quot; to confirm
              </label>
              <input
                type="text"
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                className="w-full px-3 py-2 text-[13px] bg-input border border-border rounded-[var(--radius-sm)] text-text-primary outline-none focus:border-brand-500 focus:shadow-[0_0_0_2px_rgba(249,115,22,0.08)]"
                autoFocus
              />
            </div>
          )}
          <div className="flex gap-2 justify-end">
            <AlertDialog.Cancel className="px-3 py-[5px] text-[12px] font-medium text-text-secondary border border-border rounded-[var(--radius-sm)] hover:bg-[rgba(255,255,255,0.03)] hover:text-text-primary transition-all duration-150">
              Cancel
            </AlertDialog.Cancel>
            <AlertDialog.Action
              disabled={!canConfirm}
              onClick={onConfirm}
              className={`px-3 py-[5px] text-[12px] font-medium rounded-[var(--radius-sm)] transition-all duration-150 disabled:opacity-50 ${
                confirmVariant === "danger"
                  ? "text-red-400 border border-[rgba(248,113,113,0.15)] hover:bg-[rgba(248,113,113,0.06)]"
                  : "bg-brand-600 text-white hover:bg-brand-700"
              }`}
            >
              {confirmLabel}
            </AlertDialog.Action>
          </div>
        </AlertDialog.Content>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  );
}
