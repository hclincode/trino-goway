import type { MessageInstance } from 'antd/es/message/interface';

/**
 * Bridge to antd's message API for code outside React render (api client,
 * session-expiry handler). The Providers tree calls `setMessageApi` once with
 * the instance from `App.useApp()`.
 */
let messageApi: MessageInstance | null = null;

export function setMessageApi(api: MessageInstance): void {
  messageApi = api;
}

export const notify = {
  success(content: string): void {
    messageApi?.success(content);
  },
  error(content: string): void {
    messageApi?.error(content);
  },
  info(content: string): void {
    messageApi?.info(content);
  },
};
