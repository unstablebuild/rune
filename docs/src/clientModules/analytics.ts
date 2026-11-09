/*
 * Google Analytics 4 for rune.build docs — consent-gated.
 *
 * GA is never loaded until the visitor grants `analytics` consent via the
 * banner in ./consent. We read window.__runeConsent on startup and listen for
 * the `runeconsentchange` event, so consent granted (or withdrawn) at any point
 * is honoured. Docusaurus is a SPA, so we also report a page_view on each
 * client-side route change.
 *
 * The measurement ID is injected at build time via the `rune-ga-measurement-id`
 * meta tag (see docusaurus.config.ts). It defaults to the staging property so
 * local and staging builds never report into prod; `npm run deploy-prod`
 * overrides it with the prod property. When no ID is configured GA stays off.
 */

import type {RuneConsent} from './consent';

const isClient = typeof window !== 'undefined';

interface GtagWindow {
  dataLayer?: unknown[];
  gtag?: (...args: unknown[]) => void;
  __runeConsent?: RuneConsent;
}

function readMeasurementId(): string {
  if (!isClient) return '';
  const meta = document.querySelector('meta[name="rune-ga-measurement-id"]');
  return meta?.getAttribute('content')?.trim() ?? '';
}

const MEASUREMENT_ID = readMeasurementId();

let loaded = false;

function loadGa(): void {
  if (loaded || !MEASUREMENT_ID) return;
  loaded = true;

  const w = window as unknown as GtagWindow;

  const script = document.createElement('script');
  script.async = true;
  script.src = `https://www.googletagmanager.com/gtag/js?id=${MEASUREMENT_ID}`;
  document.head.appendChild(script);

  w.dataLayer = w.dataLayer || [];
  // Defined exactly like Google's official snippet: push the raw Arguments
  // object so gtag.js receives the command tuples in the form it expects.
  w.gtag = function gtag() {
    // eslint-disable-next-line prefer-rest-params
    (w.dataLayer as unknown[]).push(arguments);
  };
  w.gtag('js', new Date());
  // Sends the initial page_view for the page GA was enabled on.
  w.gtag('config', MEASUREMENT_ID);
}

function apply(consent: RuneConsent | undefined): void {
  if (consent?.analytics) {
    loadGa();
  } else if (loaded) {
    // Consent withdrawn after GA loaded: stop GA from sending further hits.
    (window as unknown as Record<string, boolean>)[
      `ga-disable-${MEASUREMENT_ID}`
    ] = true;
  }
}

if (isClient && MEASUREMENT_ID) {
  // Attach the listener before reading the current value so we can't miss a
  // consent change published between the two.
  window.addEventListener('runeconsentchange', (e) => {
    apply((e as CustomEvent<RuneConsent>).detail);
  });
  apply((window as unknown as GtagWindow).__runeConsent);
}

// Report SPA navigations. The initial page_view is sent by gtag('config').
export function onRouteDidUpdate({
  location,
  previousLocation,
}: {
  location: {pathname: string; search: string; hash: string};
  previousLocation: {pathname: string; search: string; hash: string} | null;
}): void {
  if (!loaded || !previousLocation) return;
  const changed =
    location.pathname !== previousLocation.pathname ||
    location.search !== previousLocation.search ||
    location.hash !== previousLocation.hash;
  if (!changed) return;
  const w = window as unknown as GtagWindow;
  w.gtag?.('event', 'page_view', {
    page_path: location.pathname + location.search + location.hash,
    page_location: window.location.href,
    page_title: document.title,
  });
}
