import React from 'react';
import clsx from 'clsx';
import {ThemeClassNames} from '@docusaurus/theme-common';
import {useDoc} from '@docusaurus/plugin-content-docs/client';
import TagsListInline from '@theme/TagsListInline';
import LastUpdated from '@theme/LastUpdated';

// The docs source lives in a private repository, so there is no public
// "Edit this page" target. Point readers at the community channel instead.
const DISCORD_URL = 'https://discord.gg/xzte9J8f8N';

function SuggestImprovement({title}: {title: string}) {
  return (
    <a
      href={DISCORD_URL}
      target="_blank"
      rel="noreferrer noopener"
      title={`See a mistake on "${title}"? Tell us on Discord`}>
      See a mistake? Tell us
    </a>
  );
}

export default function DocItemFooter(): React.ReactNode {
  const {metadata} = useDoc();
  const {lastUpdatedAt, lastUpdatedBy, tags, title} = metadata;
  const canDisplayTagsRow = tags.length > 0;
  return (
    <footer
      className={clsx(ThemeClassNames.docs.docFooter, 'docusaurus-mt-lg')}>
      {canDisplayTagsRow && (
        <div
          className={clsx(
            'row margin-top--sm',
            ThemeClassNames.docs.docFooterTagsRow,
          )}>
          <div className="col">
            <TagsListInline tags={tags} />
          </div>
        </div>
      )}
      <div
        className={clsx(
          'row margin-top--sm',
          ThemeClassNames.docs.docFooterEditMetaRow,
        )}>
        <div className="col">
          <SuggestImprovement title={title} />
        </div>
        <div className="col text--right">
          {(lastUpdatedAt || lastUpdatedBy) && (
            <LastUpdated
              lastUpdatedAt={lastUpdatedAt}
              lastUpdatedBy={lastUpdatedBy}
            />
          )}
        </div>
      </div>
    </footer>
  );
}
