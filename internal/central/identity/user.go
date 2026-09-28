package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	userdb "github.com/ChargePilot2026/charge-pilot/internal/central/identity/generated"
	"github.com/go-sql-driver/mysql"
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

type UserStore struct{ DB *sql.DB }

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
	if err := userdb.New(s.DB).CreateLoginIdentity(ctx, []byte(openID)); err != nil {
		return User{}, err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	q := userdb.New(tx)
	if _, err := q.LockLoginIdentity(ctx, []byte(openID)); err != nil {
		return User{}, err
	}
	current, err := q.FindActiveUserByOpenID(ctx, openID)
	if err == nil {
		if current.Status != userdb.UserStatusActive {
			return User{}, ErrUserFrozen
		}
		if err := q.TouchUserLogin(ctx, userdb.TouchUserLoginParams{ID: current.ID, LastLoginAt: sql.NullTime{Time: time.Now().UTC(), Valid: true}}); err != nil {
			return User{}, err
		}
		if err := tx.Commit(); err != nil {
			return User{}, err
		}
		return User{ID: current.ID, OpenID: openID}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return User{}, err
	}
	result, err := q.CreateUser(ctx, userdb.CreateUserParams{Openid: openID, Unionid: sql.NullString{String: unionID, Valid: unionID != ""}, LastLoginAt: sql.NullTime{Time: time.Now().UTC(), Valid: true}})
	if err != nil {
		return User{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return User{}, err
	}
	if err := q.CreateWallet(ctx, uint64(id)); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return User{ID: uint64(id), OpenID: openID, IsNew: true}, nil
}

func (s UserStore) Active(ctx context.Context, id uint64) (bool, error) {
	status, err := userdb.New(s.DB).GetUserStatus(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("user status: %w", err)
	}
	return status == userdb.UserStatusActive, nil
}

func (s UserStore) Profile(ctx context.Context, id uint64) (Profile, error) {
	row, err := userdb.New(s.DB).GetProfile(ctx, id)
	if err != nil {
		return Profile{}, err
	}
	profile := Profile{UserID: row.ID, Nickname: row.Nickname.String, AvatarURL: row.AvatarUrl.String, PhoneBound: row.PhoneHash.Valid, RegisteredAt: row.FirstSeenAt.UTC(), CouponUnusedCount: row.CouponUnusedCount}
	profile.Wallet.AvailableCents = row.BalanceCents
	profile.Wallet.FrozenCents = row.FrozenCents
	var cardType string
	switch value := row.CardType.(type) {
	case string:
		cardType = value
	case []byte:
		cardType = string(value)
	}
	if cardType != "" {
		profile.MembershipCard = map[string]string{"card_type": cardType}
	}
	return profile, nil
}
