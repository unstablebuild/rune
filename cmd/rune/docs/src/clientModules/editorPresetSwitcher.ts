type EditorPreset = 'modal' | 'helix' | 'standard' | 'emacs';
type EffectiveEditor = EditorPreset | 'exo';
type Platform = 'darwin' | 'linux';
type PresetSelection =
  | 'standard-darwin'
  | 'standard-linux'
  | 'modal'
  | 'helix'
  | 'emacs';

interface PresetDef {
  id: PresetSelection;
  editor: EditorPreset;
  label: string;
  platform?: Platform;
}

interface FixedGuide {
  id: EffectiveEditor;
  label: string;
}

interface RuneConsent {
  functional: boolean;
}

const PRESETS: PresetDef[] = [
  {
    id: 'standard-darwin',
    editor: 'standard',
    label: 'Standard (macOS)',
    platform: 'darwin',
  },
  {
    id: 'standard-linux',
    editor: 'standard',
    label: 'Standard (Linux)',
    platform: 'linux',
  },
  {id: 'modal', editor: 'modal', label: 'Vim'},
  {id: 'helix', editor: 'helix', label: 'Helix'},
  {id: 'emacs', editor: 'emacs', label: 'Emacs'},
];
const FIXED_GUIDES: Record<string, FixedGuide> = {
  '/learn/exoeditor': {id: 'exo', label: 'Exoeditor'},
  '/learn/vim-editor': {id: 'modal', label: 'Vim'},
  '/learn/helix-editor': {id: 'helix', label: 'Helix'},
  '/learn/standard-editor': {id: 'standard', label: 'Standard'},
  '/learn/emacs-editor': {id: 'emacs', label: 'Emacs'},
};
const STORAGE_KEY = 'rune-editor-preset';
const CHANGE_EVENT = 'runeeditorpresetchange';
const isClient = typeof window !== 'undefined';

function canPersist(): boolean {
  if (!isClient) return false;
  const consent = (window as unknown as {__runeConsent?: RuneConsent})
    .__runeConsent;
  return !!consent?.functional;
}

function defaultSelection(): PresetSelection {
  return detectPlatform() === 'darwin' ? 'standard-darwin' : 'standard-linux';
}

function loadPreset(): PresetSelection {
  if (!isClient || !canPersist()) return defaultSelection();
  try {
    const value = window.localStorage.getItem(STORAGE_KEY);
    if (PRESETS.some((preset) => preset.id === value)) {
      return value as PresetSelection;
    }
    // Migrate the original platform-detected Standard selection.
    if (value === 'standard') return defaultSelection();
  } catch {
    /* ignore */
  }
  return defaultSelection();
}

function savePreset(preset: PresetSelection): void {
  if (!isClient || !canPersist()) return;
  try {
    window.localStorage.setItem(STORAGE_KEY, preset);
  } catch {
    /* ignore */
  }
}

function detectPlatform(): Platform {
  if (!isClient) return 'darwin';
  const platform = navigator.platform || navigator.userAgent;
  return /Mac|iPhone|iPad|iPod/i.test(platform) ? 'darwin' : 'linux';
}

function currentGuide(): FixedGuide | null {
  if (!isClient) return null;
  const path = window.location.pathname.replace(/\/$/, '') || '/';
  return FIXED_GUIDES[path] ?? null;
}

let selectedPreset: PresetSelection = defaultSelection();
const renderers = new Set<() => void>();

function selectedDefinition(): PresetDef {
  return PRESETS.find((preset) => preset.id === selectedPreset) ?? PRESETS[0];
}

function effectiveSelection(): {
  editor: EffectiveEditor;
  platform: Platform;
  label: string;
} {
  const guide = currentGuide();
  const selected = selectedDefinition();
  if (!guide) {
    return {
      editor: selected.editor,
      platform: selected.platform ?? detectPlatform(),
      label: selected.label,
    };
  }
  if (guide.id === 'standard') {
    const standard =
      selected.editor === 'standard'
        ? selected
        : PRESETS.find((preset) => preset.id === defaultSelection())!;
    return {
      editor: 'standard',
      platform: standard.platform!,
      label: standard.label,
    };
  }
  return {editor: guide.id, platform: detectPlatform(), label: guide.label};
}

function publish(): void {
  if (!isClient) return;
  const {editor, platform} = effectiveSelection();
  document.documentElement.setAttribute('data-rune-editor-preset', editor);
  document.documentElement.setAttribute('data-rune-platform', platform);
  window.dispatchEvent(
    new CustomEvent(CHANGE_EVENT, {detail: {editor, platform}}),
  );
  for (const render of renderers) render();
}

function selectPreset(preset: PresetSelection, persist: boolean): void {
  selectedPreset = preset;
  if (persist) savePreset(preset);
  publish();
}

function mountInto(slot: HTMLElement): void {
  if (slot.dataset.mounted === '1') return;
  slot.dataset.mounted = '1';

  const wrap = document.createElement('div');
  wrap.className = 'rune-editor-preset-switcher';

  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'rune-editor-preset-switcher__btn';

  const label = document.createElement('span');
  button.append(label);

  const render = (): void => {
    if (!slot.isConnected) {
      renderers.delete(render);
      return;
    }
    const fixed = currentGuide();
    const active = effectiveSelection();
    label.textContent = `Preset: ${active.label}`;
    button.disabled = fixed !== null && fixed.id !== 'standard';
    if (fixed && fixed.id !== 'standard') {
      button.title = `This guide always shows ${fixed.label} keybindings`;
      button.setAttribute(
        'aria-label',
        `This guide always shows ${fixed.label} keybindings`,
      );
    } else {
      button.title = `Editor preset: ${active.label}. Click for next`;
      button.setAttribute(
        'aria-label',
        `Cycle editor preset. Current: ${active.label}`,
      );
    }
  };

  renderers.add(render);
  button.addEventListener('click', () => {
    const fixed = currentGuide();
    if (fixed && fixed.id !== 'standard') return;
    if (fixed?.id === 'standard') {
      const next =
        effectiveSelection().platform === 'darwin'
          ? 'standard-linux'
          : 'standard-darwin';
      selectPreset(next, true);
      return;
    }
    const currentIndex = PRESETS.findIndex((preset) => preset.id === selectedPreset);
    selectPreset(PRESETS[(currentIndex + 1) % PRESETS.length].id, true);
  });

  wrap.append(button);
  slot.append(wrap);
  render();
}

function mountAll(): void {
  if (!isClient) return;
  document
    .querySelectorAll<HTMLElement>('.rune-editor-preset-switcher-slot')
    .forEach((slot) => mountInto(slot));
}

function mountWithRetry(remaining = 30): void {
  if (!isClient) return;
  mountAll();
  const anyUnmounted = document.querySelector(
    '.rune-editor-preset-switcher-slot:not([data-mounted="1"])',
  );
  if (anyUnmounted && remaining > 0) {
    window.requestAnimationFrame(() => mountWithRetry(remaining - 1));
  }
}

if (isClient) {
  selectedPreset = loadPreset();
  publish();

  window.addEventListener('runeconsentchange', () => {
    if (canPersist()) savePreset(selectedPreset);
  });

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', () => mountWithRetry());
  } else {
    mountWithRetry();
  }

  const observer = new MutationObserver(() => {
    const unmounted = document.querySelector(
      '.rune-editor-preset-switcher-slot:not([data-mounted="1"])',
    );
    if (unmounted) mountAll();
  });
  observer.observe(document.body, {childList: true, subtree: true});
}

export function onRouteDidUpdate(): void {
  publish();
  mountAll();
}
