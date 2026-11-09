import React from 'react';
import Link from '@docusaurus/Link';
import useBaseUrl from '@docusaurus/useBaseUrl';

// Floating call-to-action that points readers at the "Docs workspace" guide,
// where they learn to ask the Rune Agent about these docs from inside Rune.
function AskRuneAgentButton(): React.ReactNode {
  const href = useBaseUrl('/learn/docs-workspace');
  return (
    <Link
      className="rune-ask-agent"
      to={href}
      aria-label="Ask Rune Agent about these docs">
      Ask Rune Agent
    </Link>
  );
}

export default function Root({children}: {children: React.ReactNode}): React.ReactNode {
  return (
    <>
      {children}
      <AskRuneAgentButton />
    </>
  );
}
