import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { StatusTag } from './StatusTag';

describe('StatusTag', () => {
  it.each([
    ['HEALTHY', 'green'],
    ['UNHEALTHY', 'red'],
    ['PENDING', 'gold'],
  ])('maps %s to the %s color', (status, color) => {
    const { container } = render(<StatusTag status={status} />);
    const tag = container.querySelector('.ant-tag');
    expect(tag).not.toBeNull();
    expect(tag?.className).toContain(`ant-tag-${color}`);
    expect(tag?.textContent).toBe(status);
  });

  it.each(['UNKNOWN', 'WEIRD'])(
    'renders %s as a default (uncolored) tag',
    (status) => {
      const { container } = render(<StatusTag status={status} />);
      const tag = container.querySelector('.ant-tag');
      expect(tag?.textContent).toBe(status);
    },
  );
});
