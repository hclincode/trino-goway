import { afterEach, describe, expect, it } from 'vitest';
import { Role, useAccessStore } from './access';

describe('access store', () => {
  afterEach(() => {
    useAccessStore.getState().clear();
  });

  it('trims and stores a token', () => {
    useAccessStore.getState().setToken('  abc  ');
    expect(useAccessStore.getState().token).toBe('abc');
    expect(useAccessStore.getState().isAuthorized()).toBe(true);
  });

  it('clearing an empty token also wipes the profile', () => {
    useAccessStore.getState().setUserInfo({
      userId: 'u1',
      userName: 'alice',
      nickName: '',
      userType: '',
      email: '',
      phonenumber: '',
      sex: '',
      avatar: '',
      permissions: ['dashboard'],
      roles: ['ADMIN'],
    });
    useAccessStore.getState().setToken('');
    expect(useAccessStore.getState().userName).toBe('');
    expect(useAccessStore.getState().roles).toEqual([]);
  });

  it('hasRole reflects the roles array', () => {
    useAccessStore.setState({ roles: ['ADMIN'] });
    expect(useAccessStore.getState().hasRole(Role.ADMIN)).toBe(true);
    expect(useAccessStore.getState().hasRole(Role.USER)).toBe(false);
  });

  it('hasPermission allows everything when the list is empty', () => {
    useAccessStore.setState({ permissions: [] });
    expect(useAccessStore.getState().hasPermission('cluster')).toBe(true);
  });

  it('hasPermission restricts to the listed keys', () => {
    useAccessStore.setState({ permissions: ['dashboard'] });
    expect(useAccessStore.getState().hasPermission('dashboard')).toBe(true);
    expect(useAccessStore.getState().hasPermission('cluster')).toBe(false);
  });
});
