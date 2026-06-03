import { Typography } from 'antd';

interface Props {
  /** Target URL; when blank, the text renders as plain text (no dead link). */
  href?: string;
  /** Visible text; defaults to the href. */
  text?: string;
}

/**
 * External link opening in a new tab. Degrades to plain text when the URL is
 * missing (Go's getAllBackends omits externalUrl when empty — gap #7).
 */
export function ExternalLink({ href, text }: Props) {
  const label = text ?? href ?? '';
  if (!href) {
    return <span>{label}</span>;
  }
  return (
    <Typography.Link href={href} target="_blank" rel="noreferrer">
      {label}
    </Typography.Link>
  );
}
