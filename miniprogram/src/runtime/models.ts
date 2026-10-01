export interface UserSession {
  token: string;
  refresh_token: string;
  jwt: string;
  jwt_expires_in: number;
}

export interface UserLogin extends UserSession {
  user_id: string;
  is_new_user: boolean;
}

export interface ChargeUserProfile {
  user_id: string;
  nickname: string;
  avatar_url: string;
  phone_bound: boolean;
  registered_at: string;
  wallet: { available_cents: number; frozen_cents: number };
  coupon_unused_count: number;
  membership_card: { card_type: string } | null;
}
