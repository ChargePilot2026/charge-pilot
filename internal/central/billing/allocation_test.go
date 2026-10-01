package billing

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
