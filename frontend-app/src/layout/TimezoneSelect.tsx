import { useMemo } from 'react';
import { Select, Typography } from 'antd';
import { useTranslation } from 'react-i18next';
import { useTimezone } from '@/context/timezone';
import { getTimeZoneOptions } from '@/utils/time';

/** Header timezone dropdown (filterable). Affects dashboard + history times. */
export function TimezoneSelect() {
  const { t } = useTranslation();
  const { timezone, changeTimezone } = useTimezone();
  const options = useMemo(
    () => getTimeZoneOptions().map((tz) => ({ value: tz, label: tz })),
    [],
  );

  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
      <Typography.Text strong>{t('dashboard.timeZone')}</Typography.Text>
      <Select
        showSearch
        style={{ width: 220 }}
        value={timezone}
        options={options}
        onChange={(v) => changeTimezone(v)}
        filterOption={(input, option) =>
          (option?.label ?? '').toLowerCase().includes(input.toLowerCase())
        }
      />
    </span>
  );
}
