// The React face of the page's one modal stack (internal/archive/assets/modal.js):
// a native <dialog> shown with showModal(), so it sits in the browser's top
// layer above every z-index, with the stack owning Escape, Tab, backdrop
// clicks, the shared scrim and focus restoration.
import "../../internal/archive/assets/modal.js";
import { useLayoutEffect, useRef, type ButtonHTMLAttributes, type DialogHTMLAttributes, type MutableRefObject, type ReactNode, type Ref } from "react";
import { Icon } from "./icons";

export type ModalDismissReason = "escape" | "backdrop";
type ModalOpenOptions = {
  opener?: HTMLElement | null;
  focus?: string | HTMLElement;
  dismiss?: false | ((reason: ModalDismissReason) => void);
  backdrop?: boolean;
  restoreFocus?: boolean | ((opener: HTMLElement | null) => HTMLElement | null | undefined);
  onClose?: (value: string) => void;
  remove?: boolean;
};
export type PharosModal = {
  open(dialog: HTMLDialogElement, options?: ModalOpenOptions): HTMLDialogElement;
  close(dialog: HTMLDialogElement | null | undefined, value?: string): void;
  layer(element: HTMLElement, options?: { dismiss?: false | ((reason: "escape") => void); opener?: HTMLElement | null }): () => void;
  top(): HTMLElement | null;
  host(anchor?: Element | null): HTMLElement;
};
declare global { interface Window { pharosModal?: PharosModal } }

/** Where floating UI anchored at anchor belongs: its open dialog, the top modal, or body. */
export function modalHost(anchor?: Element | null): HTMLElement {
  return window.pharosModal?.host(anchor) ?? anchor?.closest<HTMLElement>("dialog[open]") ?? document.body;
}
/** The topmost modal surface, or null. */
export function topModal(): HTMLElement | null {
  return window.pharosModal?.top() ?? null;
}

type ModalProps = Omit<DialogHTMLAttributes<HTMLDialogElement>, "open" | "onClose" | "onCancel"> & {
  /** Defaults to true: a mounted Modal is shown. */
  open?: boolean;
  /** After the dialog closed, for any reason. Not called when it unmounts open. */
  onClose: () => void;
  /** Escape or a backdrop click asked to close; the default closes the dialog. */
  onDismiss?: (reason: ModalDismissReason) => void;
  /** False while the dialog must stay (a save in flight): Escape and the backdrop do nothing. */
  dismissible?: boolean;
  /** False keeps a backdrop click from dismissing (forms that would lose a draft). */
  backdropDismiss?: boolean;
  /** Selector or element focused on open; the browser's default otherwise. */
  initialFocus?: string;
  /** The control focus returns to; defaults to what had focus when it opened. */
  opener?: HTMLElement | null;
  /** Where focus goes on close, given the opener; false leaves it alone. */
  restoreFocus?: boolean | ((opener: HTMLElement | null) => HTMLElement | null | undefined);
  dialogRef?: Ref<HTMLDialogElement>;
  children?: ReactNode;
};

/** A modal dialog on the shared stack. */
export function Modal({ open = true, onClose, onDismiss, dismissible = true, backdropDismiss = true, initialFocus, opener, restoreFocus = true, dialogRef, children, ...props }: ModalProps) {
  const dialog = useRef<HTMLDialogElement | null>(null);
  const latest = useRef({ onClose, onDismiss, dismissible, backdropDismiss, restoreFocus, silent: false });
  Object.assign(latest.current, { onClose, onDismiss, dismissible, backdropDismiss, restoreFocus });
  useLayoutEffect(() => {
    const element = dialog.current;
    if (!element) return;
    if (!open) { if (element.open) element.close(); return; }
    latest.current.silent = false;
    window.pharosModal!.open(element, {
      opener, focus: initialFocus,
      dismiss: reason => {
        const current = latest.current;
        if (!current.dismissible || (reason === "backdrop" && !current.backdropDismiss)) return;
        if (current.onDismiss) current.onDismiss(reason);
        else element.close();
      },
      restoreFocus: given => {
        const choice = latest.current.restoreFocus;
        return typeof choice === "function" ? choice(given) : choice ? given : null;
      },
      onClose: () => { if (!latest.current.silent) latest.current.onClose(); },
    });
    return () => {
      // Unmounting or hiding an open dialog closes it without reporting back:
      // the owner already changed the state that hid it.
      latest.current.silent = true;
      if (element.open) element.close();
    };
  }, [open]);
  const setRef = (element: HTMLDialogElement | null) => {
    dialog.current = element;
    if (typeof dialogRef === "function") dialogRef(element);
    else if (dialogRef) (dialogRef as MutableRefObject<HTMLDialogElement | null>).current = element;
  };
  return <dialog ref={setRef} {...props}>{children}</dialog>;
}

/** The standard close button for a modal's header. */
export function ModalClose({ label, className = "", ...props }: Omit<ButtonHTMLAttributes<HTMLButtonElement>, "type" | "aria-label" | "title" | "children"> & { label: string }) {
  return <button type="button" className={`modal-close ${className}`.trim()} aria-label={label} title={label} {...props}><Icon name="close" /></button>;
}
