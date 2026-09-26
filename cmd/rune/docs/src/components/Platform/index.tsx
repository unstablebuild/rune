import React from 'react';
import {
  useEditorSelection,
  type Platform as PlatformName,
} from '@site/src/components/editorSelection';

export interface PlatformProps {
  // darwin or linux.
  when: PlatformName;
  children: React.ReactNode;
}

// Platform renders its children only for the platform selected in the
// switcher, so a guide shows the keys a reader actually presses instead of
// a column per platform. The server renders the macOS variant.
export default function Platform({when, children}: PlatformProps): React.ReactNode {
  const {platform} = useEditorSelection();
  if (platform !== when) return null;
  return <>{children}</>;
}