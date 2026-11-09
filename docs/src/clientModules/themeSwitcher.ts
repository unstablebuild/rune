type ThemeId =
  | 'romero'
  | 'furman'
  | 'carmack'
  | 'reynolds'
  | 'brevik'
  | 'thompson'
  | 'pike'
  | 'hopper'
  | 'kernighan'
  | 'wozniak'
  | 'auge'
  | 'ritchie'
  | 'kelleher'
  | 'sanfilippo'
  | 'frost'
  | 'mullen';

interface ThemeDef {
  id: ThemeId;
  label: string;
  swatch: string;
}

// Swatch = the theme's "blue" slot, which we map to --rune-orange
// (the primary accent / CTA colour for that palette).
const THEMES: ThemeDef[] = [
  {id: 'romero', label: 'Romero', swatch: '#BA0E2E'},
  {id: 'furman', label: 'Furman', swatch: '#808080'},
  {id: 'carmack', label: 'Carmack', swatch: '#D79921'},
  {id: 'reynolds', label: 'Reynolds', swatch: '#32CD32'},
  {id: 'brevik', label: 'Brevik', swatch: '#8700FF'},
  {id: 'thompson', label: 'Thompson', swatch: '#508AAA'},
  {id: 'pike', label: 'Pike', swatch: '#003366'},
  {id: 'hopper', label: 'Hopper', swatch: '#454545'},
  {id: 'kernighan', label: 'Kernighan', swatch: '#3B78FF'},
  {id: 'wozniak', label: 'Wozniak', swatch: '#6346FF'},
  {id: 'auge', label: 'Auge', swatch: '#FFCC33'},
  {id: 'ritchie', label: 'Ritchie', swatch: '#2F5AF3'},
  {id: 'kelleher', label: 'Kelleher', swatch: '#9D65FF'},
  {id: 'sanfilippo', label: 'Sanfilippo', swatch: '#C1E3FE'},
  {id: 'frost', label: 'Frost', swatch: '#D7AFFF'},
  {id: 'mullen', label: 'Mullen', swatch: '#9A77B1'},
];

const STORAGE_KEY = 'rune-theme';

const isClient = typeof window !== 'undefined';

interface RuneConsent {
  functional: boolean;
}

function canPersist(): boolean {
  if (!isClient) return false;
  const c = (window as unknown as {__runeConsent?: RuneConsent}).__runeConsent;
  return !!c?.functional;
}

function loadTheme(): ThemeId {
  if (!isClient) return 'romero';
  if (!canPersist()) return 'romero';
  try {
    const v = window.localStorage.getItem(STORAGE_KEY);
    if (v && THEMES.some((t) => t.id === v)) return v as ThemeId;
  } catch {
    /* ignore */
  }
  return 'romero';
}

function saveTheme(id: ThemeId): void {
  if (!isClient || !canPersist()) return;
  try {
    window.localStorage.setItem(STORAGE_KEY, id);
  } catch {
    /* ignore */
  }
}

function applyTheme(id: ThemeId): void {
  if (!isClient) return;
  document.documentElement.setAttribute('data-rune-theme', id);
}

function findTheme(id: ThemeId): ThemeDef {
  return THEMES.find((t) => t.id === id) ?? THEMES[0];
}

// Shared state so multiple mounted slots (desktop nav + mobile sidebar)
// and the consent listener stay in sync.
let currentIdx = 0;
const renderers: Array<() => void> = [];

function setIndex(idx: number, persist: boolean): void {
  currentIdx = (idx + THEMES.length) % THEMES.length;
  const t = THEMES[currentIdx];
  applyTheme(t.id);
  if (persist) saveTheme(t.id);
  for (const r of renderers) r();
}

function mountInto(slot: HTMLElement): void {
  if (slot.dataset.mounted === '1') return;
  slot.dataset.mounted = '1';

  const wrap = document.createElement('div');
  wrap.className = 'rune-theme-switcher';

  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'rune-theme-switcher__btn';
  btn.setAttribute('aria-label', 'Cycle theme');

  const swatch = document.createElement('span');
  swatch.className = 'rune-theme-switcher__swatch';

  const label = document.createElement('span');
  label.className = 'rune-theme-switcher__label';

  btn.append(swatch, label);

  const render = (): void => {
    const t = THEMES[currentIdx];
    swatch.style.background = t.swatch;
    label.textContent = t.label;
    btn.title = `Theme: ${t.label} — click for next`;
  };
  renderers.push(render);

  btn.addEventListener('click', () => setIndex(currentIdx + 1, true));

  render();
  wrap.append(btn);
  slot.appendChild(wrap);
}

function mountAll(): void {
  if (!isClient) return;
  document
    .querySelectorAll<HTMLElement>('.rune-theme-switcher-slot')
    .forEach((slot) => mountInto(slot));
}

// React hydration races us on the initial paint, so keep trying for
// a few frames until the placeholder appears (or we give up).
function mountWithRetry(remaining = 30): void {
  if (!isClient) return;
  mountAll();
  const anyUnmounted = document.querySelector(
    '.rune-theme-switcher-slot:not([data-mounted="1"])',
  );
  if (anyUnmounted && remaining > 0) {
    window.requestAnimationFrame(() => mountWithRetry(remaining - 1));
  }
}

if (isClient) {
  // Initialise currentIdx from storage *if* consent has already been
  // captured by the consent module before us. Otherwise we'll resync
  // when the `runeconsentchange` event fires.
  currentIdx = Math.max(
    0,
    THEMES.findIndex((t) => t.id === loadTheme()),
  );
  applyTheme(THEMES[currentIdx].id);

  window.addEventListener('runeconsentchange', () => {
    // If functional consent was just granted, persist the current
    // pick. If it was withdrawn, the consent module has already
    // cleared the storage; nothing more to do here.
    if (canPersist()) saveTheme(THEMES[currentIdx].id);
  });

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', () => mountWithRetry());
  } else {
    mountWithRetry();
  }

  // Docusaurus destroys/rebuilds the navbar (and our slot) when the
  // mobile/desktop breakpoint flips. Without this observer the slot
  // comes back empty after a resize because mountWithRetry already
  // finished. Watch the DOM and mount into any new slots that appear.
  const observer = new MutationObserver(() => {
    const unmounted = document.querySelector(
      '.rune-theme-switcher-slot:not([data-mounted="1"])',
    );
    if (unmounted) mountAll();
  });
  observer.observe(document.body, {childList: true, subtree: true});
}

// Re-mount after client-side route changes (the placeholder gets re-rendered).
export function onRouteDidUpdate(): void {
  mountAll();
}
