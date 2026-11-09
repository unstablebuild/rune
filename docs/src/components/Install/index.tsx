import React, {useCallback, useEffect, useRef, useState} from 'react';
import styles from './styles.module.css';

const RELEASES = 'https://github.com/unstablebuild/rune/releases/latest/download';
const DARWIN_ARM64 = `${RELEASES}/Rune-darwin-arm64.dmg`;
const DARWIN_AMD64 = `${RELEASES}/Rune-darwin-amd64.dmg`;
const INSTALL_CMD = 'curl -fsSL https://rune.build/install.sh | sh';

function fallbackCopy(text: string): boolean {
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.top = '-1000px';
    document.body.appendChild(ta);
    ta.select();
    document.execCommand('copy');
    document.body.removeChild(ta);
    return true;
  } catch {
    return false;
  }
}

export default function Install(): React.JSX.Element {
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onMouseDown = (e: MouseEvent) => {
      const target = e.target as Node | null;
      if (rootRef.current && target && !rootRef.current.contains(target)) {
        setOpen(false);
      }
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onMouseDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('mousedown', onMouseDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [open]);

  const onCopy = useCallback(() => {
    const done = () => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1400);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(INSTALL_CMD).then(done, () => {
        if (fallbackCopy(INSTALL_CMD)) done();
      });
    } else if (fallbackCopy(INSTALL_CMD)) {
      done();
    }
  }, []);

  return (
    <div className={styles.ctaRow}>
      <div className={styles.dlMac} ref={rootRef}>
        <button
          className={`${styles.btn} ${styles.primary} ${styles.dlMacTrigger}`}
          type="button"
          aria-haspopup="menu"
          aria-expanded={open}
          onClick={(e) => {
            e.preventDefault();
            e.stopPropagation();
            setOpen((v) => !v);
          }}
        >
          <span>Download for Mac</span>
          <span className={styles.dlChevron} aria-hidden="true">▾</span>
        </button>
        <div className={styles.dlMacMenu} role="menu" hidden={!open}>
          <a
            className={styles.dlMacItem}
            role="menuitem"
            href={DARWIN_ARM64}
            download
          >
            Apple Silicon
          </a>
          <a
            className={styles.dlMacItem}
            role="menuitem"
            href={DARWIN_AMD64}
            download
          >
            Intel
          </a>
        </div>
      </div>

      <div className={styles.install} role="group" aria-label="Linux install command">
        <span className={styles.prompt} aria-hidden="true">$</span>
        <code className={styles.installCode}>{INSTALL_CMD}</code>
        <button
          className={`${styles.copy} ${copied ? styles.ok : ''}`}
          aria-label="Copy install command"
          type="button"
          onClick={onCopy}
        >
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
    </div>
  );
}
