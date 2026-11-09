import React from 'react';
import MDXComponents from '@theme-original/MDXComponents';
import KeyBinding, {CommandPromptKey} from '@site/src/components/KeyBinding';
import Platform from '@site/src/components/Platform';
import Preset from '@site/src/components/Preset';

export default {
  ...MDXComponents,
  KeyBinding,
  CommandPromptKey,
  Platform,
  Preset,
} satisfies Record<
  string,
  React.ComponentType<never> | keyof React.JSX.IntrinsicElements
>;
