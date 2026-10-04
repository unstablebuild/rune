type EditorPreset = 'modal' | 'helix' | 'standard' | 'emacs';
type EffectiveEditor = EditorPreset | 'exo';
type Platform = 'darwin' | 'linux';

interface PresetDef {
  id: EditorPreset;
  label: string;
}

interface FixedGuide {
  id: EffectiveEditor;
  label: string;
}

interface RuneConsent {
  functional: boolean;
}

const PRESETS: PresetDef[] = [
  {id: 'standard', label: 'Standard'},
  {id: 'modal', label: 'Vim'},
  {id: 'helix', label: 'Helix'},
  {id: 'emacs', label: 'Emacs'},
];
const PLATFORM_LABELS: Record<Platform, string> = {
  darwin: 'macOS',
  linux: 'Linux',
};
// Each editor guide documents one editor, so its preset is fixed; the
// platform still follows the switcher, since every editor ships a macOS and
// a Linux preset.
const FIXED_GUIDES: Record<string, FixedGuide> = {
  '/learn/exoeditor': {id: 'exo', label: 'Exoeditor'},
  '/learn/vim-editor': {id: 'modal', label: 'Vim'},
  '/learn/helix-editor': {id: 'helix', label: 'Helix'},
  '/learn/standard-editor': {id: 'standard', label: 'Standard'},
  '/learn/emacs-editor': {id: 'emacs', label: 'Emacs'},
};
const PRESET_KEY = 'rune-editor-preset';
const PLATFORM_KEY = 'rune-platform';
const CHANGE_EVENT = 'runeeditorpresetchange';
const isClient = typeof window !== 'undefined';

function canPersist(): boolean {
  if (!isClient) return false;
  const consent = (window as unknown as {__runeConsent?: RuneConsent})
    .__runeConsent;
  return !!consent?.functional;
}

function readStored(key: string): string | null {
  if (!canPersist()) return null;
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function store(key: string, value: string): void {
  if (!canPersist()) return;
  try {
    window.localStorage.setItem(key, value);
  } catch {
    /* ignore */
  }
}

// Only a Mac runs the macOS build, so phones, tablets (iPadOS reports a Mac
// platform but has touch points) and Windows get the Linux keys.
function detectPlatform(): Platform {
  if (!isClient) return 'darwin';
  const platform = navigator.platform || navigator.userAgent;
  const touch = navigator.maxTouchPoints > 1;
  return /Mac/i.test(platform) && !touch ? 'darwin' : 'linux';
}

function currentGuide(): FixedGuide | null {
  if (!isClient) return null;
  const path = window.location.pathname.replace(/\/$/, '') || '/';
  return FIXED_GUIDES[path] ?? null;
}

let selectedEditor: EditorPreset = 'standard';
// null follows the browser's platform until the reader picks one.
let selectedPlatform: Platform | null = null;
const renderers = new Set<() => void>();

// Values from before the platform had its own switch, such as
// "standard-linux", fall back to Standard on the detected platform.
function load(): void {
  const editor = readStored(PRESET_KEY);
  const preset = PRESETS.find((p) => p.id === editor);
  if (preset) selectedEditor = preset.id;
  const platform = readStored(PLATFORM_KEY);
  if (platform === 'darwin' || platform === 'linux') selectedPlatform = platform;
}

function save(): void {
  store(PRESET_KEY, selectedEditor);
  if (selectedPlatform) store(PLATFORM_KEY, selectedPlatform);
}

function effectiveSelection(): {
  editor: EffectiveEditor;
  label: string;
  platform: Platform;
} {
  const platform = selectedPlatform ?? detectPlatform();
  const guide = currentGuide();
  if (guide) return {editor: guide.id, label: guide.label, platform};
  const preset =
    PRESETS.find((p) => p.id === selectedEditor) ?? PRESETS[0];
  return {editor: preset.id, label: preset.label, platform};
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

function makeButton(): [HTMLButtonElement, HTMLSpanElement] {
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'rune-editor-preset-switcher__btn';
  const label = document.createElement('span');
  button.append(label);
  return [button, label];
}

function mountInto(slot: HTMLElement): void {
  if (slot.dataset.mounted === '1') return;
  slot.dataset.mounted = '1';

  const wrap = document.createElement('div');
  wrap.className = 'rune-editor-preset-switcher';
  const [presetButton, presetLabel] = makeButton();
  const [platformButton, platformLabel] = makeButton();

  const render = (): void => {
    if (!slot.isConnected) {
      renderers.delete(render);
      return;
    }
    const guide = currentGuide();
    const active = effectiveSelection();
    presetLabel.textContent = `Preset: ${active.label}`;
    presetButton.disabled = guide !== null;
    const presetHint = guide
      ? `This guide always shows ${guide.label} keybindings`
      : `Editor preset: ${active.label}. Click for next`;
    presetButton.title = presetHint;
    presetButton.setAttribute('aria-label', presetHint);

    const current = PLATFORM_LABELS[active.platform];
    const other = PLATFORM_LABELS[active.platform === 'darwin' ? 'linux' : 'darwin'];
    platformLabel.textContent = `Platform: ${current}`;
    const platformHint = `Showing ${current} keybindings. Click for ${other}`;
    platformButton.title = platformHint;
    platformButton.setAttribute('aria-label', platformHint);
  };

  renderers.add(render);
  presetButton.addEventListener('click', () => {
    if (currentGuide()) return;
    const index = PRESETS.findIndex((p) => p.id === selectedEditor);
    selectedEditor = PRESETS[(index + 1) % PRESETS.length].id;
    save();
    publish();
  });
  platformButton.addEventListener('click', () => {
    selectedPlatform =
      effectiveSelection().platform === 'darwin' ? 'linux' : 'darwin';
    save();
    publish();
  });

  wrap.append(presetButton, platformButton);
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
  load();
  publish();

  window.addEventListener('runeconsentchange', () => {
    if (canPersist()) save();
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