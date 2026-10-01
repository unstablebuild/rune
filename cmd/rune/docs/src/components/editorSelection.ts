import {useSyncExternalStore} from 'react';

export type EditorPreset = 'modal' | 'helix' | 'standard' | 'emacs';
export type Platform = 'darwin' | 'linux';

export interface Selection {
  preset: EditorPreset;
  platform: Platform;
}

const CHANGE_EVENT = 'runeeditorpresetchange';

// The switcher only exists on the client, so the server renders the
// default selection and hydration corrects it.
const SERVER_SELECTION = 'standard:darwin';

function readSelection(): string {
  if (typeof document === 'undefined') return SERVER_SELECTION;
  const preset = document.documentElement.getAttribute(
    'data-rune-editor-preset',
  );
  const platform = document.documentElement.getAttribute('data-rune-platform');
  const safePreset: EditorPreset =
    preset === 'modal' || preset === 'helix' || preset === 'emacs'
      ? preset
      : 'standard';
  const safePlatform: Platform = platform === 'linux' ? 'linux' : 'darwin';
  return `${safePreset}:${safePlatform}`;
}

function subscribe(onChange: () => void): () => void {
  if (typeof window === 'undefined') return () => undefined;
  window.addEventListener(CHANGE_EVENT, onChange);
  return () => window.removeEventListener(CHANGE_EVENT, onChange);
}

export function useEditorSelection(): Selection {
  const value = useSyncExternalStore(
    subscribe,
    readSelection,
    () => SERVER_SELECTION,
  );
  const [preset, platform] = value.split(':') as [EditorPreset, Platform];
  return {preset, platform};
}
