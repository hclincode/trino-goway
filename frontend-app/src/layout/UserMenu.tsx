import { useState } from 'react';
import { Button, Dropdown } from 'antd';
import {
  LogoutOutlined,
  SettingOutlined,
  UserOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { notify } from '@/app/notify';
import { Role, useAccessStore } from '@/stores/access';
import { useLogoutMutation } from '@/features/auth/queries';
import { ProfileModal } from './ProfileModal';

/** Header user dropdown: Profile + Logout. Icon differs for ADMIN. */
export function UserMenu() {
  const { t } = useTranslation();
  const isAdmin = useAccessStore((s) => s.hasRole(Role.ADMIN));
  const logoutMutation = useLogoutMutation();
  const [profileOpen, setProfileOpen] = useState(false);

  const onLogout = async () => {
    try {
      await logoutMutation.mutateAsync();
      notify.success(t('auth.logoutSuccess'));
    } catch {
      // logout clears locally regardless; nothing else to surface
    }
  };

  return (
    <>
      <Dropdown
        trigger={['click']}
        menu={{
          items: [
            {
              key: 'profile',
              icon: <UserOutlined />,
              label: t('menu.profile'),
              onClick: () => setProfileOpen(true),
            },
            {
              key: 'logout',
              icon: <LogoutOutlined />,
              label: t('menu.logout'),
              onClick: onLogout,
            },
          ],
        }}
      >
        <Button
          type="text"
          aria-label="User menu"
          icon={
            isAdmin ? (
              <SettingOutlined style={{ color: '#fa8c16' }} />
            ) : (
              <UserOutlined style={{ color: '#1677ff' }} />
            )
          }
        />
      </Dropdown>
      <ProfileModal open={profileOpen} onClose={() => setProfileOpen(false)} />
    </>
  );
}
