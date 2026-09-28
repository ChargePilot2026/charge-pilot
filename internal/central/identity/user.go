package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrUserFrozen = errors.New("user account frozen")

type User struct {
	ID     uint64
	OpenID string
	IsNew  bool
}

type Profile struct {
	UserID       uint64    `json:"user_id"`
	Nickname     string    `json:"nickname"`
	AvatarURL    string    `json:"avatar_url"`
	PhoneBound   bool      `json:"phone_bound"`
	RegisteredAt time.Time `json:"registered_at"`
	Wallet       struct {
		AvailableCents int64 `json:"available_cents"`
		FrozenCents    int64 `json:"frozen_cents"`
	} `json:"wallet"`
	CouponUnusedCount int64 `json:"coupon_unused_count"`
	MembershipCard    any   `json:"membership_card"`
}

type UserStore struct{ DB *gorm.DB }

func (s UserStore) Login(ctx context.Context, openID, unionID string) (User, error) {
	if openID == "" || len(openID) > 64 || len(unionID) > 64 {
		return User{}, errors.New("invalid WeChat identity")
	}
	for attempt := 0; attempt < 5; attempt++ {
		user, err := s.loginOnce(ctx, openID, unionID)
		if !retryableMySQL(err) {
			return user, err
		}
		select {
		case <-ctx.Done():
			return User{}, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 20 * time.Millisecond):
		}
	}
	return User{}, errors.New("concurrent login retries exhausted")
}

func retryableMySQL(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && (mysqlErr.Number == 1213 || mysqlErr.Number == 1205)
}

func (s UserStore) loginOnce(ctx context.Context, openID, unionID string) (User, error) {
	// Create the identity row in autocommit mode. Concurrent INSERT IGNORE
	// inside transactions can deadlock on the missing-key gap before FOR UPDATE.
	if err := s.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&LoginIdentity{OpenID: []byte(openID)}).Error; err != nil {
		return User{}, err
	}
	var user User
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var identity LoginIdentity
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("openid = ?", []byte(openID)).Take(&identity).Error; err != nil {
			return err
		}
		var current UserAccount
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("openid = ? AND deleted_at IS NULL", openID).Order("id ASC").Take(&current).Error
		if err == nil {
			if current.Status != "active" {
				return ErrUserFrozen
			}
			now := time.Now().UTC()
			if err := tx.Model(&UserAccount{}).Where("id = ?", current.ID).Update("last_login_at", now).Error; err != nil {
				return err
			}
			user = User{ID: current.ID, OpenID: openID}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := time.Now().UTC()
		created := UserAccount{OpenID: openID, Status: "active", LastLoginAt: sql.NullTime{Time: now, Valid: true}}
		if unionID != "" {
			created.UnionID = sql.NullString{String: unionID, Valid: true}
		}
		if err := tx.Create(&created).Error; err != nil {
			return err
		}
		if err := tx.Create(&WalletAccount{UserID: created.ID}).Error; err != nil {
			return err
		}
		user = User{ID: created.ID, OpenID: openID, IsNew: true}
		return nil
	})
	return user, err
}

func (s UserStore) Active(ctx context.Context, id uint64) (bool, error) {
	var user UserAccount
	err := s.DB.WithContext(ctx).Select("status").Where("id = ? AND deleted_at IS NULL", id).Take(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("user status: %w", err)
	}
	return user.Status == "active", nil
}

func (s UserStore) Profile(ctx context.Context, id uint64) (Profile, error) {
	var row profileRow
	result := s.DB.WithContext(ctx).Raw(`SELECT u.id, u.nickname, u.avatar_url, u.phone_hash, u.first_seen_at,
		w.balance_cents, w.frozen_cents,
		(SELECT COUNT(*) FROM coupon_grant AS g WHERE g.user_id = u.id AND g.status = 'unused' AND g.expired_at > NOW(3) AND g.deleted_at IS NULL) AS coupon_unused_count,
		COALESCE(CAST((SELECT card_type FROM membership_card AS m WHERE m.user_id = u.id AND m.status = 'active' AND m.end_at > NOW(3) AND m.deleted_at IS NULL ORDER BY m.end_at DESC LIMIT 1) AS CHAR(16)), '') AS card_type
		FROM user AS u JOIN wallet_account AS w ON w.user_id = u.id AND w.deleted_at IS NULL
		WHERE u.id = ? AND u.status = 'active' AND u.deleted_at IS NULL LIMIT 1`, id).Scan(&row)
	if result.Error != nil {
		return Profile{}, result.Error
	}
	if result.RowsAffected != 1 {
		return Profile{}, gorm.ErrRecordNotFound
	}
	profile := Profile{UserID: row.ID, Nickname: row.Nickname.String, AvatarURL: row.AvatarURL.String, PhoneBound: row.PhoneHash.Valid, RegisteredAt: row.FirstSeenAt.UTC(), CouponUnusedCount: row.CouponUnusedCount}
	profile.Wallet.AvailableCents = row.BalanceCents
	profile.Wallet.FrozenCents = row.FrozenCents
	cardType := row.CardType
	if cardType != "" {
		profile.MembershipCard = map[string]string{"card_type": cardType}
	}
	return profile, nil
}

type LoginIdentity struct {
	OpenID []byte `gorm:"column:openid;primaryKey"`
}

func (LoginIdentity) TableName() string { return "user_login_identity" }

type UserAccount struct {
	ID          uint64         `gorm:"column:id;primaryKey"`
	OpenID      string         `gorm:"column:openid"`
	UnionID     sql.NullString `gorm:"column:unionid"`
	Status      string         `gorm:"column:status"`
	LastLoginAt sql.NullTime   `gorm:"column:last_login_at"`
}

func (UserAccount) TableName() string { return "user" }

type WalletAccount struct {
	ID     uint64 `gorm:"column:id;primaryKey"`
	UserID uint64 `gorm:"column:user_id"`
}

func (WalletAccount) TableName() string { return "wallet_account" }

type profileRow struct {
	ID                uint64
	Nickname          sql.NullString
	AvatarURL         sql.NullString
	PhoneHash         sql.NullString
	FirstSeenAt       time.Time
	BalanceCents      int64
	FrozenCents       int64
	CouponUnusedCount int64
	CardType          string
}
