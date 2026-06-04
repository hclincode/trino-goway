import { Avatar, Modal, Space, Tag, Typography } from 'antd';
import { IdcardOutlined } from '@ant-design/icons';
import { Role, useAccessStore } from '@/stores/access';
import { useConfigStore } from '@/stores/config';

interface Props {
  open: boolean;
  onClose: () => void;
}

/** User profile dialog: avatar, username, user id, role tags. */
export function ProfileModal({ open, onClose }: Props) {
  const { userName, userId, avatar, roles } = useAccessStore();
  const fallbackAvatar = useConfigStore((s) => s.avatar);

  return (
    <Modal
      open={open}
      onCancel={onClose}
      onOk={onClose}
      footer={null}
      width={400}
      centered
    >
      <Space direction="vertical" align="center" style={{ width: '100%' }} size="middle">
        <Avatar size={72} src={avatar || fallbackAvatar} />
        <Typography.Title level={4} style={{ margin: 0 }}>
          {userName}
        </Typography.Title>
        <Typography.Text type="secondary">
          <IdcardOutlined /> {userId}
        </Typography.Text>
        <Space wrap>
          {roles.map((role) => (
            <Tag
              key={role}
              color={role === Role.ADMIN ? 'orange' : 'blue'}
            >
              {role}
            </Tag>
          ))}
        </Space>
      </Space>
    </Modal>
  );
}
