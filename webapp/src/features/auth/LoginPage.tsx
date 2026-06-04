import { useState } from 'react';
import { Button, Form, Input, Spin, Typography } from 'antd';
import { useTranslation } from 'react-i18next';
import { notify } from '@/app/notify';
import {
  useLoginMutation,
  useLoginType,
  useSsoMutation,
} from './queries';
import styles from './LoginPage.module.css';

export default function LoginPage() {
  const { t } = useTranslation();
  const { data: loginType } = useLoginType();
  const loginMutation = useLoginMutation();
  const ssoMutation = useSsoMutation();
  const [form] = Form.useForm<{ username: string; password: string }>();
  const [submitting, setSubmitting] = useState(false);

  const onFormSubmit = async (values: {
    username: string;
    password: string;
  }) => {
    setSubmitting(true);
    try {
      await loginMutation.mutateAsync({
        username: values.username,
        password: values.password ?? '',
      });
      notify.success(t('auth.loginSuccess'));
    } catch (err) {
      notify.error(err instanceof Error ? err.message : t('error.network'));
    } finally {
      setSubmitting(false);
    }
  };

  const onSso = () => {
    ssoMutation.mutate();
  };

  return (
    <div className={styles.main}>
      <div className={styles.card}>
        <img src="/trino-gateway/logo.svg" alt="Trino Gateway" className={styles.logo} />
        <h1 className={styles.heading}>
          {t('auth.tip1')}
          {t('auth.tip2')}
          {t('auth.tip3')}
        </h1>

        {loginType === undefined && (
          <div className={styles.spinner}>
            <Spin size="large" />
          </div>
        )}

        {(loginType === 'form' || loginType === 'none') && (
          <Form
            form={form}
            layout="vertical"
            className={styles.form}
            onFinish={onFormSubmit}
          >
            <Form.Item
              name="username"
              label={t('auth.username')}
              rules={[{ required: true, message: t('auth.usernameTip') }]}
            >
              <Input placeholder={t('auth.usernameTip')} autoComplete="username" />
            </Form.Item>
            <Form.Item
              name="password"
              label={t('auth.password')}
              rules={
                loginType === 'form'
                  ? [{ required: true, message: t('auth.passwordTip') }]
                  : []
              }
            >
              <Input.Password
                placeholder={
                  loginType === 'none' ? t('auth.noneAuthTip') : t('auth.passwordTip')
                }
                readOnly={loginType === 'none'}
                autoComplete="current-password"
              />
            </Form.Item>
            <Button
              type="primary"
              htmlType="submit"
              className={styles.button}
              loading={submitting}
            >
              {t('auth.login')}
            </Button>
          </Form>
        )}

        {loginType === 'oauth' && (
          <Button
            type="primary"
            className={styles.button}
            loading={ssoMutation.isPending}
            onClick={onSso}
          >
            {t('auth.oauth2')}
          </Button>
        )}

        {loginType === undefined && (
          <Typography.Text type="secondary">{t('auth.loginTitle')}</Typography.Text>
        )}
      </div>
    </div>
  );
}
