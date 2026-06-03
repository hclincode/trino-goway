import { useEffect, useMemo, useState } from 'react';
import { Modal } from 'antd';
import hljs from 'highlight.js/lib/core';
import sql from 'highlight.js/lib/languages/sql';
import 'highlight.js/styles/github.css';
import { useTranslation } from 'react-i18next';
import { copyToClipboard } from '@/utils/clipboard';
import styles from './SqlModal.module.css';

hljs.registerLanguage('sql', sql);

interface Props {
  open: boolean;
  sqlText: string;
  onClose: () => void;
}

/** Full query text: SQL syntax highlight, line numbers, word-wrap, copy button. */
export function SqlModal({ open, sqlText, onClose }: Props) {
  const { t } = useTranslation();
  const [okText, setOkText] = useState<string>(t('copy.button'));

  useEffect(() => {
    if (open) setOkText(t('copy.button'));
  }, [open, t]);

  // Highlight per line so we can render line numbers alongside wrapped content.
  const lines = useMemo(() => {
    const highlighted = hljs.highlight(sqlText, { language: 'sql' }).value;
    return highlighted.split('\n');
  }, [sqlText]);

  const onCopy = async () => {
    const ok = await copyToClipboard(sqlText);
    setOkText(ok ? t('copy.success') : t('copy.failed'));
  };

  return (
    <Modal
      open={open}
      title="Query Text"
      width={800}
      okText={okText}
      cancelText={t('ui.close')}
      onOk={onCopy}
      onCancel={onClose}
    >
      <pre className={styles.viewer}>
        <table className={styles.table}>
          <tbody>
            {lines.map((line, i) => (
              <tr key={i}>
                <td className={styles.lineNo}>{i + 1}</td>
                <td
                  className={`hljs ${styles.lineContent}`}
                  // highlight.js output is trusted, generated from the query text.
                  dangerouslySetInnerHTML={{ __html: line || ' ' }}
                />
              </tr>
            ))}
          </tbody>
        </table>
      </pre>
    </Modal>
  );
}
