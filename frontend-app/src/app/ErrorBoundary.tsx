import { Component, type ErrorInfo, type ReactNode } from 'react';
import { Result, Typography } from 'antd';

interface Props {
  children: ReactNode;
}

interface State {
  error: Error | null;
  componentStack: string | null;
}

/**
 * App-wide error boundary. On a render crash, shows the error message and the
 * component stack (matches the original behavior; does not attempt recovery).
 */
export class ErrorBoundary extends Component<Props, State> {
  override state: State = { error: null, componentStack: null };

  static getDerivedStateFromError(error: Error): Partial<State> {
    return { error };
  }

  override componentDidCatch(_error: Error, info: ErrorInfo): void {
    this.setState({ componentStack: info.componentStack ?? null });
  }

  override render(): ReactNode {
    const { error, componentStack } = this.state;
    if (!error) return this.props.children;

    return (
      <Result
        status="error"
        title="Oops, something went wrong!"
        subTitle={error.message}
      >
        {componentStack && (
          <Typography.Paragraph>
            <pre style={{ whiteSpace: 'pre-wrap', overflow: 'auto' }}>
              {componentStack}
            </pre>
          </Typography.Paragraph>
        )}
      </Result>
    );
  }
}
