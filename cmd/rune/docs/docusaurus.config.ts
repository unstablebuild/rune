import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
import tabBlocks from 'docusaurus-remark-plugin-tab-blocks';
import {readFileSync} from 'node:fs';
import {load} from 'js-yaml';

// This runs in Node.js - Don't use client-side code here (browser APIs, JSX...)

// GA4 measurement ID, injected into the page via the headTags meta tag below
// and read by src/clientModules/analytics.ts. Defaults to the staging property
// so local dev and staging deploys never report into prod analytics;
// `npm run deploy-prod` overrides GA_MEASUREMENT_ID with the prod property.
const STAGING_GA_MEASUREMENT_ID = ''; // TODO: set to the staging G-XXXXXXXX
const GA_MEASUREMENT_ID =
  process.env.GA_MEASUREMENT_ID || STAGING_GA_MEASUREMENT_ID;
const KEYBINDINGS = load(
  readFileSync(new URL('./src/data/keybindings.yaml', import.meta.url), 'utf8'),
);

const config: Config = {
  title: 'Rune',
  tagline: 'The development environment for pros',
  favicon: 'img/favicon.ico',

  // Future flags, see https://docusaurus.io/docs/api/docusaurus-config#future
  future: {
    v4: true, // Improve compatibility with the upcoming Docusaurus v4
  },

  url: 'https://docs.rune.build',
  baseUrl: '/',

  organizationName: 'unstablebuild',
  projectName: 'docs-rune',

  onBrokenLinks: 'throw',

  markdown: {mermaid: true},

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  customFields: {
    // Generated from Rune's preset YAML by `make generate`.
    keybindings: KEYBINDINGS,
  },

  // Preload JetBrains Mono so the typographic identity matches rune.build.
  stylesheets: [
    'https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500;600;700&display=swap',
  ],

  // Expose the GA4 measurement ID to the client (read by
  // src/clientModules/analytics.ts). Omitted when no property is configured, so
  // GA stays off for local and staging until a staging ID is set.
  headTags: GA_MEASUREMENT_ID
    ? [
        {
          tagName: 'meta',
          attributes: {
            name: 'rune-ga-measurement-id',
            content: GA_MEASUREMENT_ID,
          },
        },
      ]
    : [],

  clientModules: [
    // consent must come before themeSwitcher so the consent state is
    // published on window before the theme switcher decides whether
    // it may persist a preference.
    './src/clientModules/consent',
    './src/clientModules/themeSwitcher',
    './src/clientModules/editorPresetSwitcher',
    // analytics reads the published consent state and only loads GA4 once
    // the visitor has granted analytics consent.
    './src/clientModules/analytics',
  ],

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          routeBasePath: '/',
          // The left sidebar already shows location, so drop the breadcrumbs.
          breadcrumbs: false,
          remarkPlugins: [
            [
              tabBlocks,
              {
                // Sync selection across all config.star / config.yaml tab blocks.
                groupId: 'rune-config',
                labels: [
                  ['python', 'config.star'],
                  ['starlark', 'config.star'],
                  ['yaml', 'config.yaml'],
                ],
              },
            ],
          ],
        },
        // Blog lives on rune.build now; keep this a docs-only site.
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themes: [
    '@docusaurus/theme-mermaid',
    [
      '@easyops-cn/docusaurus-search-local',
      {
        hashed: true,
        indexBlog: false,
        docsRouteBasePath: '/',
      },
    ],
  ],

  themeConfig: {
    image: 'https://assets.rune.build/images/docs-rune/og.jpg',
    // Rune is a dark-only brand. Lock the docs to dark mode.
    colorMode: {
      defaultMode: 'dark',
      disableSwitch: true,
      respectPrefersColorScheme: false,
    },
    navbar: {
      title: '',
      logo: {
        alt: 'Rune',
        src: 'https://assets.rune.build/images/rune-logo-layout-transparent-150x150',
      },
      hideOnScroll: false,
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'tutorialSidebar',
          position: 'left',
          label: 'Docs',
        },
        {
          type: 'search',
          position: 'right',
        },
        {
          type: 'html',
          position: 'right',
          // Filled in client-side by editorPresetSwitcher.ts
          value: '<div class="rune-editor-preset-switcher-slot"></div>',
        },
        {
          type: 'html',
          position: 'right',
          // Filled in client-side by src/clientModules/themeSwitcher.ts
          value: '<div class="rune-theme-switcher-slot"></div>',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Product',
          items: [
            {label: 'Releases', href: 'https://rune.build/releases'},
            {label: 'Docs', to: '/'},
          ],
        },
        {
          title: 'Resources',
          items: [
            {label: 'FAQ', href: 'https://rune.build/faq'},
            {label: 'Support', href: 'https://rune.build/support'},
            {label: 'Blog', href: 'https://rune.build/blog'},
            {
              html:
                '<a id="cookie-preferences" href="#cookie-preferences" class="footer__link-item footer__cookie-link">Cookie preferences</a>',
            },
          ],
        },
        {
          title: 'Company',
          items: [
            {label: 'About', href: 'https://rune.build/about'},
            {
              label: 'Jobs',
              href: 'https://www.linkedin.com/company/unstablebuild/jobs/',
            },
            {label: 'Press', href: 'mailto:press@unstable.build'},
          ],
        },
        {
          title: 'Community',
          items: [
            {label: 'GitHub', href: 'https://github.com/unstablebuild'},
            {label: 'Discord', href: 'https://discord.gg/xzte9J8f8N'},
            {label: 'Hugging Face', href: 'https://huggingface.co/unstablebuild'},
            {label: 'X', href: 'https://x.com/unstablebuild'},
          ],
        },
      ],
      copyright: `Unstable Build LLC. · © ${new Date().getFullYear()}`,
    },
    prism: {
      theme: prismThemes.oneDark,
      darkTheme: prismThemes.oneDark,
      additionalLanguages: ['bash', 'diff', 'json', 'go', 'rust', 'typescript'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
