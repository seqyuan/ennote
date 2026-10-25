/**
 * One Escape stack for every dismissible surface.
 *
 * The stack listens on the window bubble phase. That ordering matters: an
 * element-level React handler that calls `stopPropagation()` (a search field
 * clearing its query, a submenu returning to its root pane) consumes Escape
 * before the event reaches the window. Only the topmost layer is dismissed, so
 * one key press can never close a dialog and its confirmation at the same time.
 *
 * No `window` access happens at module scope: Next runs with `output: "export"`
 * and renders client components during the static build.
 */

export interface KeyboardEventLike {
  key: string;
  defaultPrevented?: boolean;
  isComposing?: boolean;
  preventDefault(): void;
}

export interface OverlayTarget {
  addEventListener(type: "keydown", listener: (event: KeyboardEventLike) => void): void;
  removeEventListener(type: "keydown", listener: (event: KeyboardEventLike) => void): void;
}

export interface OverlayLayer {
  id: string;
  onDismiss: () => void;
  /** `false` keeps the layer open on Escape (blocking prompts). */
  dismissOnEscape?: boolean;
}

export interface OverlayStack {
  push(layer: OverlayLayer): () => void;
  dismissTop(event?: KeyboardEventLike): boolean;
  reset(): void;
  size(): number;
}

export function createOverlayStack(target?: OverlayTarget): OverlayStack {
  let layers: OverlayLayer[] = [];
  let listening = false;

  const onKeyDown = (event: KeyboardEventLike): void => {
    if (event.key !== "Escape" || event.defaultPrevented || event.isComposing) return;
    const top = layers[layers.length - 1];
    if (!top || top.dismissOnEscape === false) return;
    event.preventDefault();
    top.onDismiss();
  };

  const attach = (): void => {
    if (!target || listening) return;
    target.addEventListener("keydown", onKeyDown);
    listening = true;
  };

  const detach = (): void => {
    if (!target || !listening) return;
    target.removeEventListener("keydown", onKeyDown);
    listening = false;
  };

  return {
    push(layer) {
      layers = layers.filter((entry) => entry.id !== layer.id);
      layers.push(layer);
      attach();
      let released = false;
      return () => {
        if (released) return;
        released = true;
        layers = layers.filter((entry) => entry !== layer);
        if (layers.length === 0) detach();
      };
    },
    dismissTop(event) {
      const top = layers[layers.length - 1];
      if (!top || top.dismissOnEscape === false) return false;
      event?.preventDefault();
      top.onDismiss();
      return true;
    },
    reset() {
      layers = [];
      detach();
    },
    size() {
      return layers.length;
    },
  };
}

function browserTarget(): OverlayTarget | undefined {
  if (typeof window === "undefined") return undefined;
  return window as unknown as OverlayTarget;
}

let defaultStack: OverlayStack | null = null;

/** Lazily create the browser stack; returns a target-less stack during SSR. */
export function getOverlayStack(): OverlayStack {
  if (defaultStack === null) defaultStack = createOverlayStack(browserTarget());
  return defaultStack;
}

export function resetOverlayStackForTesting(): void {
  defaultStack?.reset();
  defaultStack = null;
}
