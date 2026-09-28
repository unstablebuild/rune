import React from 'react';
import MDXComponents from '@theme-original/MDXComponents';
import KeyBinding, {CommandPromptKey} from '@site/src/components/KeyBinding';
import Preset from '@site/src/components/Preset';

export default {
  ...MDXComponents,
  KeyBinding,
  CommandPromptKey,
  Preset,
} satisfies Record<
  string,
  React.ComponentType<never> | keyof React.JSX.IntrinsicElements
>;
