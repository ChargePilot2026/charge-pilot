-- name: CreateLoginIdentity :exec
INSERT IGNORE INTO user_login_identity (openid) VALUES (?);

-- name: LockLoginIdentity :one
SELECT openid FROM user_login_identity WHERE openid = ? FOR UPDATE;

-- name: FindActiveUserByOpenID :one
SELECT id, openid, status FROM user
WHERE openid = ? AND deleted_at IS NULL
ORDER BY id LIMIT 1 FOR UPDATE;

-- name: CreateUser :execresult
INSERT INTO user (openid, unionid, last_login_at) VALUES (?, ?, ?);

-- name: TouchUserLogin :exec
UPDATE user SET last_login_at = ? WHERE id = ?;

-- name: CreateWallet :exec
INSERT INTO wallet_account (user_id) VALUES (?);

-- name: GetUserStatus :one
SELECT status FROM user WHERE id = ? AND deleted_at IS NULL LIMIT 1;

-- name: GetProfile :one
SELECT u.id, u.nickname, u.avatar_url, u.phone_hash, u.first_seen_at,
       w.balance_cents, w.frozen_cents,
       (SELECT COUNT(*) FROM coupon_grant AS g WHERE g.user_id = u.id AND g.status = 'unused' AND g.expired_at > NOW(3) AND g.deleted_at IS NULL) AS coupon_unused_count,
       COALESCE(CAST((SELECT card_type FROM membership_card AS m WHERE m.user_id = u.id AND m.status = 'active' AND m.end_at > NOW(3) AND m.deleted_at IS NULL ORDER BY m.end_at DESC LIMIT 1) AS CHAR(16)), '') AS card_type
FROM user AS u JOIN wallet_account AS w ON w.user_id = u.id AND w.deleted_at IS NULL
WHERE u.id = ? AND u.status = 'active' AND u.deleted_at IS NULL
LIMIT 1;
