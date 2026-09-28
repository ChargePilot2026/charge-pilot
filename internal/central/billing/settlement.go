package billing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/finance"
	"gorm.io/gorm"
)

var ErrNoSplitTemplate = errors.New("站点未配置分账模板，无法生成分账")

// SplitTemplate resolves the station's active allocation contract. Central owns
// admin_db, so the template is read there and never through cross-schema SQL.
type SplitTemplate struct {
	ID      uint64
	Code    string
	Mode    string
	Parties []finance.Party
	Names   map[string]string
	// IDs maps each party code to its split_party row id, because the settlement
	// ledger stores the numeric party id and the code separately.
	IDs map[string]uint64
}

// SplitResolver reads admin_db on behalf of the billing module.
type SplitResolver struct{ AdminDB *gorm.DB }

// Resolve returns the active template bound to the station. A missing, deleted,
// disabled, or internally inconsistent template is an error rather than a silent
// zero split, so a station can never be billed without a recorded allocation.
func (r SplitResolver) Resolve(ctx context.Context, stationID uint64) (SplitTemplate, error) {
	if r.AdminDB == nil {
		return SplitTemplate{}, errors.New("split resolver is not configured")
	}
	var row struct {
		ID         uint64
		Code       string
		Mode       string
		StationID  *uint64
		DeletedAt  sql.NullTime
		PartyCount int
	}
	query := r.AdminDB.WithContext(ctx).Table("split_template AS t").
		Joins("JOIN station AS s ON s.split_template_id = t.id AND s.deleted_at IS NULL").
		Where("s.id = ? AND s.deleted_at IS NULL AND t.deleted_at IS NULL AND t.status = 'active'", stationID).
		Select("t.id, t.code, t.mode, s.id AS station_id, t.deleted_at, (SELECT COUNT(*) FROM split_party p WHERE p.split_template_id = t.id) AS party_count").
		Take(&row)
	if query.Error != nil {
		return SplitTemplate{}, query.Error
	}
	type partyRow struct {
		ID        uint64
		PartyCode string
		PartyName string
		RatioBP   uint32
	}
	parties := []partyRow{}
	if err := r.AdminDB.WithContext(ctx).Table("split_party").Where("split_template_id = ?", row.ID).Order("party_code").Find(&parties).Error; err != nil {
		return SplitTemplate{}, err
	}
	if len(parties) < 2 || len(parties) > 8 {
		return SplitTemplate{}, fmt.Errorf("%w: template %d has %d parties", ErrNoSplitTemplate, row.ID, len(parties))
	}
	out := SplitTemplate{ID: row.ID, Code: row.Code, Mode: row.Mode, Parties: make([]finance.Party, 0, len(parties)), Names: map[string]string{}, IDs: map[string]uint64{}}
	for _, p := range parties {
		out.Parties = append(out.Parties, finance.Party{ID: p.PartyCode, RatioBPS: int64(p.RatioBP)})
		out.Names[p.PartyCode] = p.PartyName
		out.IDs[p.PartyCode] = p.ID
	}
	// Reject an inconsistent template at resolve time so a bad ratio cannot
	// reach the settlement ledger.
	mode := finance.SplitAll
	if row.Mode == "mode_b" {
		mode = finance.SplitServiceOnly
	} else if row.Mode != "mode_a" {
		return SplitTemplate{}, fmt.Errorf("%w: template %d has unknown mode %q", ErrNoSplitTemplate, row.ID, row.Mode)
	}
	if _, err := finance.Allocate(0, 0, mode, out.Parties); err != nil {
		return SplitTemplate{}, fmt.Errorf("%w: template %d ratios: %v", ErrNoSplitTemplate, row.ID, err)
	}
	return out, nil
}

// Settle writes the allocation for one calculated fee. The unique key on
// (fee_calculation_id, generation) makes a replayed billing dispatch return the
// existing settlement instead of paying the parties twice.
func (s Store) Settle(ctx context.Context, calculationID uint64, template SplitTemplate, fee pricing.ActualFee, month time.Time) (uint64, bool, error) {
	mode := finance.SplitAll
	if template.Mode == "mode_b" {
		mode = finance.SplitServiceOnly
	}
	allocation, err := finance.Allocate(finance.Money(fee.ElectricCents), finance.Money(fee.ServiceCents), mode, template.Parties)
	if err != nil {
		return 0, false, err
	}
	var pool, allocated int64
	for _, share := range allocation.Shares {
		allocated += int64(share.ElectricCents) + int64(share.ServiceCents)
	}
	if mode == finance.SplitAll {
		pool = fee.ElectricCents + fee.ServiceCents
	} else {
		pool = fee.ServiceCents
	}
	// Every cent of the split pool must reach exactly one party; otherwise the
	// ledger and the fee record would disagree.
	if allocated != pool || pool < 0 || fee.TotalCents < pool {
		return 0, false, fmt.Errorf("allocation does not preserve the split pool: %d != %d", allocated, pool)
	}
	created := true
	var settlementID uint64
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var calculation struct {
			ID        uint64  `gorm:"column:id"`
			OrderNo   string  `gorm:"column:order_no"`
			StationID *uint64 `gorm:"column:station_id"`
			Total     int64   `gorm:"column:total_cents"`
		}
		if err := tx.Table("fee_calculation").Where("id = ?", calculationID).Take(&calculation).Error; err != nil {
			return err
		}
		if calculation.StationID == nil {
			return ErrNoSplitTemplate
		}
		if calculation.Total != fee.TotalCents {
			return ErrConflict
		}
		var existing struct {
			ID         uint64 `gorm:"column:id"`
			SplitPool  int64  `gorm:"column:split_pool_cents"`
			Electric   int64  `gorm:"column:electric_cents"`
			Service    int64  `gorm:"column:service_cents"`
			TemplateID uint64 `gorm:"column:split_template_id"`
		}
		found := tx.Table("settlement").Where("fee_calculation_id = ? AND generation = 1", calculationID).Take(&existing)
		if found.Error != nil && !errors.Is(found.Error, gorm.ErrRecordNotFound) {
			return found.Error
		}
		if found.RowsAffected > 0 {
			if existing.SplitPool != pool || existing.Electric != fee.ElectricCents || existing.Service != fee.ServiceCents || existing.TemplateID != template.ID {
				return ErrConflict
			}
			settlementID, created = existing.ID, false
			return nil
		}
		no := fmt.Sprintf("STL%020d", calculationID)
		row := map[string]any{
			"settlement_no": no, "split_template_id": template.ID, "split_template_code": template.Code,
			"mode": template.Mode, "fee_calculation_id": calculationID, "generation": 1, "order_no": calculation.OrderNo,
			"total_cents": fee.TotalCents, "electric_cents": fee.ElectricCents, "service_cents": fee.ServiceCents,
			"split_pool_cents": pool, "split_pool_excluded_electric_cents": allocation.ElectricExcludedCents,
			"status": "pending", "created_month": month,
		}
		if err := tx.Table("settlement").Create(row).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&settlementID).Error; err != nil {
			return err
		}
		rows := make([]map[string]any, 0, len(allocation.Shares))
		for i, share := range allocation.Shares {
			rows = append(rows, map[string]any{
				"settlement_id": settlementID, "party_id": template.IDs[share.PartyID], "party_code": share.PartyID,
				"party_name": template.Names[share.PartyID], "ratio_bp": template.Parties[i].RatioBPS,
				"amount_cents": share.ElectricCents + share.ServiceCents, "electric_cents": share.ElectricCents,
				"service_cents": share.ServiceCents, "status": "pending", "generation": 1,
			})
		}
		return tx.Table("settlement_party_amount").Create(&rows).Error
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, false, err
	}
	return settlementID, created, nil
}

// SettlementsDue lists calculated fees that have no settlement yet. Billing and
// settlement share one billing_db, so this never crosses schemas.
func (s Store) SettlementsDue(ctx context.Context, limit int) ([]PendingSettlement, error) {
	if limit <= 0 {
		limit = 50
	}
	rows := []PendingSettlement{}
	err := s.DB.WithContext(ctx).Table("fee_calculation AS f").
		Joins("LEFT JOIN settlement st ON st.fee_calculation_id = f.id AND st.generation = 1").
		Where("st.id IS NULL").
		Select("f.id, f.station_id, f.total_cents, f.electric_cents, f.service_cents").
		Order("f.created_at, f.id").Limit(limit).Find(&rows).Error
	return rows, err
}

type PendingSettlement struct {
	CalculationID uint64  `gorm:"column:id"`
	StationID     *uint64 `gorm:"column:station_id"`
	TotalCents    int64   `gorm:"column:total_cents"`
	ElectricCents int64   `gorm:"column:electric_cents"`
	ServiceCents  int64   `gorm:"column:service_cents"`
}

// CalculationMonth reads the partition month a calculation was filed under so a
// later replay writes the settlement into the same partition.
func (s Store) CalculationMonth(ctx context.Context, calculationID uint64) (time.Time, error) {
	var month time.Time
	// Select the column explicitly: a bare Take would scan every column of the
	// partitioned fee_calculation row into a single destination.
	err := s.DB.WithContext(ctx).Table("fee_calculation").Select("created_month").Where("id = ?", calculationID).Take(&month).Error
	return month, err
}
