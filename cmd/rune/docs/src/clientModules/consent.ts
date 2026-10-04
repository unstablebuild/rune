/*
 * GDPR-friendly consent for rune.build docs.
 *
 * Storage: a single localStorage key `rune-consent` containing
 * { version, functional, analytics, marketing, ts }.
 *
 * Other modules (e.g. themeSwitcher) read `window.__runeConsent`
 * and listen for the `runeconsentchange` event before persisting
 * anything to localStorage.
 */

export interface RuneConsent {
  version: number;
  functional: boolean;
  analytics: boolean;
  marketing: boolean;
  ts: number;
}

const STORAGE_KEY = 'rune-consent';
const CONSENT_VERSION = 1;
const EVENT_NAME = 'runeconsentchange';

const isClient = typeof window !== 'undefined';

const DEFAULT: RuneConsent = {
  version: CONSENT_VERSION,
  functional: false,
  analytics: false,
  marketing: false,
  ts: 0,
};

interface Category {
  id: 'functional' | 'analytics' | 'marketing';
  label: string;
  desc: string;
}

const CATEGORIES: Category[] = [
  {
    id: 'functional',
    label: 'Functional',
    desc:
      'Remembers your chosen theme and editor preset between visits. Stored only in your browser.',
  },
  {
    id: 'analytics',
    label: 'Analytics',
    desc:
      'Anonymous usage statistics via Google Analytics (GA4). Loaded only if you allow it.',
  },
  {
    id: 'marketing',
    label: 'Marketing',
    desc:
      'Personalised advertising. None are currently in use on this site.',
  },
];

function loadConsent(): RuneConsent | null {
  if (!isClient) return null;
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<RuneConsent>;
    if (parsed.version !== CONSENT_VERSION) return null;
    return {
      version: CONSENT_VERSION,
      functional: !!parsed.functional,
      analytics: !!parsed.analytics,
      marketing: !!parsed.marketing,
      ts: typeof parsed.ts === 'number' ? parsed.ts : Date.now(),
    };
  } catch {
    return null;
  }
}

function persist(c: RuneConsent): void {
  if (!isClient) return;
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(c));
  } catch {
    /* ignore */
  }
}

function publish(c: RuneConsent): void {
  (window as unknown as {__runeConsent?: RuneConsent}).__runeConsent = c;
  window.dispatchEvent(new CustomEvent(EVENT_NAME, {detail: c}));
}

function applyAndPublish(c: RuneConsent): void {
  persist(c);
  publish(c);
  // If a category was revoked, clean up storage owned by that category.
  if (!c.functional) {
    try {
      window.localStorage.removeItem('rune-theme');
      window.localStorage.removeItem('rune-editor-preset');
      window.localStorage.removeItem('rune-platform');
    } catch {
      /* ignore */
    }
  }
}

function makeButton(label: string, onClick: () => void): HTMLButtonElement {
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'rune-consent-btn';
  btn.textContent = label;
  btn.addEventListener('click', onClick);
  return btn;
}

function makeRow(
  category: Category | {id: 'essential'; label: string; desc: string},
  locked: boolean,
  initial: boolean,
  onChange: (v: boolean) => void,
): HTMLElement {
  const row = document.createElement('div');
  row.className = 'rune-consent-row';

  const text = document.createElement('div');
  text.className = 'rune-consent-row__text';
  const label = document.createElement('div');
  label.className = 'rune-consent-row__label';
  label.textContent = category.label;
  const desc = document.createElement('div');
  desc.className = 'rune-consent-row__desc';
  desc.textContent = category.desc;
  text.append(label, desc);

  const toggleWrap = document.createElement('label');
  toggleWrap.className = 'rune-consent-toggle';
  if (locked) toggleWrap.classList.add('rune-consent-toggle--locked');

  const input = document.createElement('input');
  input.type = 'checkbox';
  input.dataset.cat = category.id;
  input.checked = initial;
  input.disabled = locked;
  input.addEventListener('change', () => onChange(input.checked));

  const visual = document.createElement('span');
  visual.className = 'rune-consent-toggle__visual';
  visual.setAttribute('aria-hidden', 'true');

  toggleWrap.append(input, visual);
  row.append(text, toggleWrap);
  return row;
}

interface ModalApi {
  el: HTMLElement;
  open: () => void;
  close: () => void;
}

function createModal(onSave: (c: RuneConsent) => void): ModalApi {
  const modal = document.createElement('div');
  modal.id = 'rune-consent-modal';
  modal.className = 'rune-consent-modal';
  modal.setAttribute('role', 'dialog');
  modal.setAttribute('aria-modal', 'true');
  modal.setAttribute('aria-labelledby', 'rune-consent-modal-title');
  modal.hidden = true;

  const backdrop = document.createElement('div');
  backdrop.className = 'rune-consent-modal__backdrop';
  backdrop.addEventListener('click', () => close());

  const card = document.createElement('div');
  card.className = 'rune-consent-modal__card';

  const title = document.createElement('h2');
  title.id = 'rune-consent-modal-title';
  title.className = 'rune-consent-modal__title';
  title.textContent = 'Cookie preferences';

  const intro = document.createElement('p');
  intro.className = 'rune-consent-modal__intro';
  intro.textContent =
    'Choose which categories of browser storage this site may use. Data is kept in your browser only and is not shared with third parties. You can change this anytime via the “Cookie preferences” link in the footer.';

  const close = (): void => {
    modal.hidden = true;
    document.body.classList.remove('rune-consent-modal-open');
  };

  const closeBtn = document.createElement('button');
  closeBtn.type = 'button';
  closeBtn.className = 'rune-consent-modal__close';
  closeBtn.setAttribute('aria-label', 'Close');
  closeBtn.textContent = '×';
  closeBtn.addEventListener('click', close);

  const list = document.createElement('div');
  list.className = 'rune-consent-modal__list';

  const draft: Pick<RuneConsent, 'functional' | 'analytics' | 'marketing'> = {
    functional: false,
    analytics: false,
    marketing: false,
  };

  list.appendChild(
    makeRow(
      {
        id: 'essential',
        label: 'Strictly necessary',
        desc:
          'Required so we can remember that you’ve set a preference here. Always on.',
      },
      true,
      true,
      () => {
        /* locked */
      },
    ),
  );

  const toggles = new Map<Category['id'], HTMLInputElement>();
  for (const cat of CATEGORIES) {
    const row = makeRow(cat, false, draft[cat.id], (v) => {
      draft[cat.id] = v;
    });
    const input = row.querySelector<HTMLInputElement>(
      `input[data-cat="${cat.id}"]`,
    );
    if (input) toggles.set(cat.id, input);
    list.appendChild(row);
  }

  const setAll = (v: boolean): void => {
    for (const cat of CATEGORIES) {
      draft[cat.id] = v;
      const input = toggles.get(cat.id);
      if (input) input.checked = v;
    }
  };

  const commit = (): void => {
    onSave({
      version: CONSENT_VERSION,
      functional: draft.functional,
      analytics: draft.analytics,
      marketing: draft.marketing,
      ts: Date.now(),
    });
    close();
  };

  const actions = document.createElement('div');
  actions.className = 'rune-consent-modal__actions';
  actions.append(
    makeButton('Reject all', () => {
      setAll(false);
      commit();
    }),
    makeButton('Save preferences', commit),
    makeButton('Accept all', () => {
      setAll(true);
      commit();
    }),
  );

  card.append(closeBtn, title, intro, list, actions);
  modal.append(backdrop, card);

  const open = (): void => {
    const current = loadConsent() ?? DEFAULT;
    for (const cat of CATEGORIES) {
      draft[cat.id] = current[cat.id];
      const input = toggles.get(cat.id);
      if (input) input.checked = current[cat.id];
    }
    modal.hidden = false;
    document.body.classList.add('rune-consent-modal-open');
  };

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !modal.hidden) close();
  });

  return {el: modal, open, close};
}

function createBanner(
  onAcceptAll: () => void,
  onRejectAll: () => void,
  onCustomize: () => void,
): HTMLElement {
  const banner = document.createElement('div');
  banner.id = 'rune-consent-banner';
  banner.className = 'rune-consent-banner';
  banner.setAttribute('role', 'region');
  banner.setAttribute('aria-label', 'Privacy notice');

  const msg = document.createElement('span');
  msg.className = 'rune-consent-banner__msg';
  msg.textContent =
    'We use a little browser storage for site preferences, and Google Analytics for anonymous usage stats if you allow it. You choose below.';

  const actions = document.createElement('div');
  actions.className = 'rune-consent-banner__actions';
  actions.append(
    makeButton('Reject all', onRejectAll),
    makeButton('Customize', onCustomize),
    makeButton('Accept all', onAcceptAll),
  );

  banner.append(msg, actions);
  return banner;
}

function removeBanner(banner: HTMLElement | null): void {
  if (!banner) return;
  banner.classList.add('rune-consent-banner--leaving');
  window.setTimeout(() => banner.remove(), 220);
}

function init(): void {
  if (!isClient) return;

  const stored = loadConsent();
  if (stored) publish(stored);

  let banner: HTMLElement | null = null;
  let modal: ModalApi | null = null;

  const handleSave = (c: RuneConsent): void => {
    applyAndPublish(c);
    if (banner) {
      removeBanner(banner);
      banner = null;
    }
  };

  modal = createModal(handleSave);
  document.body.appendChild(modal.el);

  // Wire up the footer "Cookie preferences" link, plus any element with
  // data-rune-open-consent so the host page can drop in their own trigger.
  document.addEventListener('click', (e) => {
    const target = e.target;
    if (!(target instanceof Element)) return;
    const trigger = target.closest<HTMLElement>(
      '[data-rune-open-consent], a[href="#cookie-preferences"]',
    );
    if (!trigger) return;
    e.preventDefault();
    modal?.open();
  });

  if (!stored) {
    banner = createBanner(
      () =>
        handleSave({
          version: CONSENT_VERSION,
          functional: true,
          analytics: true,
          marketing: true,
          ts: Date.now(),
        }),
      () =>
        handleSave({
          version: CONSENT_VERSION,
          functional: false,
          analytics: false,
          marketing: false,
          ts: Date.now(),
        }),
      () => modal?.open(),
    );
    document.body.appendChild(banner);
    window.requestAnimationFrame(() =>
      banner?.classList.add('rune-consent-banner--visible'),
    );
  }
}

if (isClient) {
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
}
