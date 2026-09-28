import React from 'react';
import {
  useEditorSelection,
  type Platform as PlatformName,
} from '@site/src/components/editorSelection';

export interface PlatformProps {
  when: PlatformName;
  children: React.ReactNode;
}

// Platform renders its children only for the platform selected in the
// switcher. Use it for keys built into an editor, which are not preset
// key bindings and so cannot be looked up with KeyBinding.
export default function Platform({
  when,
  children,
}: PlatformProps): React.ReactNode {
  const {platform} = useEditorSelection();
  if (platform !== when) return null;
  return <>{children}</>;
}