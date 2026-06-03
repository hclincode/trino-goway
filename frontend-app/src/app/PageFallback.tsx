import { Spin } from 'antd';

/** Suspense fallback for lazily-loaded route pages. */
export function PageFallback() {
  return (
    <div style={{ display: 'flex', justifyContent: 'center', padding: 48 }}>
      <Spin size="large" />
    </div>
  );
}
