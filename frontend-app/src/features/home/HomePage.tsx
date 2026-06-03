import { Empty, Typography } from 'antd';
import { useTranslation } from 'react-i18next';
import { useAccessStore } from '@/stores/access';

/** Fallback (404 / idle) page: greeting for the signed-in user. */
export default function HomePage() {
  const { t } = useTranslation();
  const userName = useAccessStore((s) => s.userName);
  return (
    <div style={{ margin: 100 }}>
      <Empty
        description={
          <Typography.Text style={{ fontSize: 18 }}>
            {t('home.welcome', { name: userName })}
          </Typography.Text>
        }
      />
    </div>
  );
}
