declare namespace Api {
  namespace Auth {
    /** Kratos 登錄響應（snake_case） */
    interface LoginToken {
      requires_totp?: boolean;
      challenge?: string;
      recovery_ticket?: string;
      access_token: string;
      refresh_token: string;
      token_type: string;
      expires_at: number;
      admin: AdminProfile;
    }

    /** Kratos 管理員信息 */
    interface AdminProfile {
      id: number;
      username: string;
      nickname: string;
      avatar: string;
      role_id: number;
      totp_enabled: boolean;
      totp_bound_at?: number | string;
      last_login_ip: string;
    }

    /** Kratos profile 響應 */
    interface UserInfo {
      admin?: AdminProfile;
      permissions?: string[];
      /** 運行時會話字段（login 後填充） */
      userId: string;
      userName: string;
      roles: string[];
      buttons: string[];
    }
  }
}
