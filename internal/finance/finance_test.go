package finance

import (
	"errors"
	"math"
	"testing"
)

func TestAllocatePreservesEveryCent(t *testing.T) {
	parties := []Party{{ID: "b", RatioBPS: 3333}, {ID: "a", RatioBPS: 3333}, {ID: "c", RatioBPS: 3334}}
	for _, mode := range []SplitMode{SplitAll, SplitServiceOnly} {
		got, err := Allocate(101, 103, mode, parties)
		if err != nil {
			t.Fatal(err)
		}
		var electric, service Money
		for _, share := range got.Shares {
			electric += share.ElectricCents
			service += share.ServiceCents
		}
		if electric+got.ElectricExcludedCents != 101 || service != 103 {
			t.Fatalf("%s: %#v", mode, got)
		}
	}
}

func TestAllocateLargeAmountDoesNotOverflow(t *testing.T) {
	got, err := Allocate(math.MaxInt64, 0, SplitAll, []Party{{ID: "a", RatioBPS: 5000}, {ID: "b", RatioBPS: 5000}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Shares[0].ElectricCents+got.Shares[1].ElectricCents != math.MaxInt64 {
		t.Fatal(got)
	}
}

func TestRejectInvalidSplit(t *testing.T) {
	_, err := Allocate(1, 1, SplitAll, []Party{{ID: "x", RatioBPS: 9000}})
	if !errors.Is(err, ErrInvalidSplit) {
		t.Fatal(err)
	}
}

func TestFundingPrecedence(t *testing.T) {
	got, err := Fund(100, 30, 40, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got != (Funding{MemberCents: 30, CouponCents: 40, WalletCents: 30, WechatCents: 0}) {
		t.Fatal(got)
	}
	got, err = Fund(100, 10, 10, 10)
	if err != nil || got.WechatCents != 70 {
		t.Fatal(got, err)
	}
}

func TestPriceEnergyKeepsFeeLinesSeparate(t *testing.T) {
	fee, err := PriceEnergy([]EnergySlice{
		{Energy: 2500, ElectricCentsPerKWh: 55, ServiceCentsPerKWh: 100},
		{Energy: 2500, ElectricCentsPerKWh: 80, ServiceCentsPerKWh: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fee != (ChargeFee{ElectricCents: 34, ServiceCents: 50, TotalCents: 84}) {
		t.Fatal(fee)
	}
}
