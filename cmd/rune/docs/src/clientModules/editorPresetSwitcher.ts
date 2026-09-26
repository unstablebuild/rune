type EditorPreset = 'modal' | 'helix' | 'standard' | 'emacs';
type EffectiveEditor = EditorPreset | 'exo';
type Platform = 'darwin' | 'linux';
// Every preset ships a macOS and a Linux variant: Super belongs to the
// desktop on Linux, so the Linux presets move Rune's layer to Alt.
type PresetSelection = `${EditorPreset}-${Platform}`;

interface PresetDef {
  id: PresetSelection;
  editor: EditorPreset;
  label: string;
  platform: Platform;
}

interface FixedGuide {
  id: EffectiveEditor;
  label: string;
}

interface RuneConsent {
  functional: boolean;
}

const EDITOR_LABELS: Record<EditorPreset, string> = {
  standard: 'Standard',
  modal: 'Vim',
  helix: 'Helix',
  emacs: 'Emacs',
};
const PLATFORM_LABELS: Record<Platform, string> = {
  darwin: 'macOS',
  linux: 'Linux',
};

function presetLabel(editor: EditorPreset, platform: Platform): string {
  return `${EDITOR_LABELS[editor]} (${PLATFORM_LABELS[platform]})`;
}

const PRESETS: PresetDef[] = (
  Object.keys(EDITOR_LABELS) as EditorPreset[]
).flatMap((editor) =>
  (['darwin', 'linux'] as Platform[]).map((platform) => ({
    id: `${editor}-${platform}` as PresetSelection,
    editor,
    label: presetLabel(editor, platform),
    platform,
  })),
);
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
  return `standard-${detectPlatform()}`;
}

function loadPreset(): PresetSelection {
  if (!isClient || !canPersist()) return defaultSelection();
  try {
    const value = window.localStorage.getItem(STORAGE_KEY);
    if (PRESETS.some((preset) => preset.id === value)) {
      return value as PresetSelection;
    }
    // Migrate selections saved before every preset had a platform.
    if (value === 'standard') return defaultSelection();
    if (value === 'modal' || value === 'helix' || value === 'emacs') {
      return `${value}-${detectPlatform()}`;
    }
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
      platform: selected.platform,
      label: selected.label,
    };
  }
  if (guide.id === 'exo') {
    return {editor: guide.id, platform: detectPlatform(), label: guide.label};
  }
  return {
    editor: guide.id,
    platform: selected.platform,
    label: presetLabel(guide.id, selected.platform),
  };
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
    button.disabled = fixed?.id === 'exo';
    if (fixed?.id === 'exo') {
      button.title = `This guide always shows ${fixed.label} keybindings`;
      button.setAttribute(
        'aria-label',
        `This guide always shows ${fixed.label} keybindings`,
      );
    } else if (fixed) {
      button.title = `${active.label}. Click to switch platform`;
      button.setAttribute(
        'aria-label',
        `Switch platform. Current: ${active.label}`,
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
    if (fixed?.id === 'exo') return;
    if (fixed) {
      // An editor guide always shows its own editor, so the button only
      // switches between that editor's macOS and Linux presets.
      const next: Platform =
        effectiveSelection().platform === 'darwin' ? 'linux' : 'darwin';
      selectPreset(`${fixed.id}-${next}`, true);
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
