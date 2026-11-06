// 登錄態（）：輕量響應式狀態（無 Pinia 依賴，單例模塊）。
import { reactive } from 'vue';
import { me } from '@/api';
import { getToken, clearToken } from '@/api/client';

export const authState = reactive({
  loggedIn: false,
  username: '',
  loaded: false
});

// refreshAuth 啓動/登錄後調用；無 token 靜默，token 失效由 client 401 層清理。
export async function refreshAuth() {
  if (!getToken()) {
    authState.loggedIn = false;
    authState.username = '';
    authState.loaded = true;
    return;
  }
  const { data } = await me();
  if (data) {
    authState.loggedIn = true;
    authState.username = data.username;
  } else {
    authState.loggedIn = false;
    authState.username = '';
  }
  authState.loaded = true;
}

export function logout() {
  clearToken();
  authState.loggedIn = false;
  authState.username = '';
}
