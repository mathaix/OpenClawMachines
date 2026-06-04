import { createContext, useCallback, useContext, useReducer } from "react";
import * as ToastPrimitive from "@radix-ui/react-toast";

type ToastVariant = "default" | "success" | "error";

interface ToastItem {
  id: string;
  title: string;
  description?: string;
  variant: ToastVariant;
}

interface ToastState {
  toasts: ToastItem[];
}

type ToastAction =
  | { type: "ADD"; toast: ToastItem }
  | { type: "REMOVE"; id: string };

const MAX_VISIBLE = 3;

function toastReducer(state: ToastState, action: ToastAction): ToastState {
  switch (action.type) {
    case "ADD": {
      const next = [action.toast, ...state.toasts];
      return { toasts: next.slice(0, MAX_VISIBLE) };
    }
    case "REMOVE":
      return { toasts: state.toasts.filter((t) => t.id !== action.id) };
  }
}

let counter = 0;

const ToastContext = createContext<{
  toast: (opts: { title: string; description?: string; variant?: ToastVariant }) => void;
} | null>(null);

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [state, dispatch] = useReducer(toastReducer, { toasts: [] });

  const toast = useCallback(
    (opts: { title: string; description?: string; variant?: ToastVariant }) => {
      const id = String(++counter);
      dispatch({
        type: "ADD",
        toast: { id, title: opts.title, description: opts.description, variant: opts.variant ?? "default" },
      });
    },
    [],
  );

  return (
    <ToastContext.Provider value={{ toast }}>
      <ToastPrimitive.Provider duration={5000}>
        {children}
        {state.toasts.map((t) => (
          <ToastPrimitive.Root
            key={t.id}
            open
            onOpenChange={(open) => {
              if (!open) dispatch({ type: "REMOVE", id: t.id });
            }}
            className={`bg-white dark:bg-gray-800 rounded-lg shadow-lg border dark:border-gray-700 p-4 border-l-4 ${variantClass(t.variant)}`}
          >
            <ToastPrimitive.Title className="text-sm font-medium text-gray-900 dark:text-gray-100">
              {t.title}
            </ToastPrimitive.Title>
            {t.description && (
              <ToastPrimitive.Description className="text-sm text-gray-500 dark:text-gray-400 mt-1">
                {t.description}
              </ToastPrimitive.Description>
            )}
          </ToastPrimitive.Root>
        ))}
        <ToastPrimitive.Viewport className="fixed bottom-4 right-4 z-50 flex flex-col gap-2 w-80" />
      </ToastPrimitive.Provider>
    </ToastContext.Provider>
  );
}

export function Toaster() {
  return null;
}

export function useToast() {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error("useToast must be used within ToastProvider");
  return ctx;
}

function variantClass(variant: ToastVariant): string {
  switch (variant) {
    case "success":
      return "border-l-green-500";
    case "error":
      return "border-l-red-500";
    default:
      return "border-l-gray-400 dark:border-l-gray-500";
  }
}
