import React from 'react';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import {
  useEditorSelection,
  type EditorPreset,
  type Platform,
} from '@site/src/components/editorSelection';

type BindingValue = string | string[];
type BindingMap = Record<string, BindingValue>;

type KeybindingData = Record<EditorPreset, Record<Platform, PresetData>>;

interface PresetData {
  command_key: string;
  key_bindings: BindingMap;
}

function commandMatches(value: BindingValue, command: BindingValue): boolean {
  if (typeof command === 'string') return value === command;
  return (
    Array.isArray(value) &&
    value.length === command.length &&
    value.every((item, index) => item === command[index])
  );
}

export interface KeyBindingProps {
  command: BindingValue;
}

export default function KeyBinding({command}: KeyBindingProps): React.ReactNode {
  const {siteConfig} = useDocusaurusContext();
  const data = siteConfig.customFields?.keybindings as unknown as KeybindingData;
  const {preset, platform} = useEditorSelection();
  const bindings = data[preset][platform].key_bindings;
  const keys = Object.entries(bindings)
    .filter(([, value]) => commandMatches(value, command))
    .map(([key]) => key);

  if (keys.length === 0) {
    return (
      <span
        className="rune-key-binding rune-key-binding--unbound"
        title={typeof command === 'string' ? command : command.join('; ')}>
        not bound by selected preset
      </span>
    );
  }

  return (
    <span
      className="rune-key-binding"
      title={typeof command === 'string' ? command : command.join('; ')}>
      {keys.map((key, index) => (
        <React.Fragment key={key}>
          {index > 0 ? ', ' : null}
          <code>{key}</code>
        </React.Fragment>
      ))}
    </span>
  );
}

export function CommandPromptKey(): React.ReactNode {
  const {siteConfig} = useDocusaurusContext();
  const data = siteConfig.customFields?.keybindings as unknown as KeybindingData;
  const {preset, platform} = useEditorSelection();
  return <code>{data[preset][platform].command_key}</code>;
}
