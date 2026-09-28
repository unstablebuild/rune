import React from 'react';
import {
  useEditorSelection,
  type EditorPreset,
} from '@site/src/components/editorSelection';

export interface PresetProps {
  // Space or comma separated presets: modal, standard, emacs.
  when: string;
  children: React.ReactNode;
}

// Preset renders its children only for the editor preset selected in
// the switcher, so a guide can state the behavior a reader actually
// has instead of enumerating every preset inline.
export default function Preset({when, children}: PresetProps): React.ReactNode {
  const {preset} = useEditorSelection();
  const wanted = when.split(/[\s,]+/).filter(Boolean) as EditorPreset[];
  if (!wanted.includes(preset)) return null;
  return <>{children}</>;
}
