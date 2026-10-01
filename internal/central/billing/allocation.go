package billing

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
)

// Money 一律以分为单位存储。
// 任何浮点数都不会进入结算。
type Money int64

type SplitMode string

const (
	SplitAll         SplitMode = "all"
	SplitServiceOnly SplitMode = "service_only"
)

type Party struct {
	ID       string
	RatioBPS int64 // 10000 = 100%
}

type Share struct {
	PartyID       string
	ElectricCents Money
	ServiceCents  Money
}

type Allocation struct {
	Shares                []Share
	ElectricExcludedCents Money
}

var ErrInvalidSplit = errors.New("invalid split configuration")

// Allocate 把电费与服务费两项各自独立拆分。
// 余数按最大余数法分配，
// 余数相同时再按 party ID 稳定排序。
func Allocate(electric, service Money, mode SplitMode, parties []Party) (Allocation, error) {
	if electric < 0 || service < 0 || len(parties) < 2 || len(parties) > 8 || (mode != SplitAll && mode != SplitServiceOnly) {
		return Allocation{}, ErrInvalidSplit
	}
	var total int64
	seen := make(map[string]bool, len(parties))
	for _, p := range parties {
		if p.ID == "" || seen[p.ID] || p.RatioBPS < 0 || p.RatioBPS > 10000 {
			return Allocation{}, ErrInvalidSplit
		}
		seen[p.ID] = true
		total += p.RatioBPS
	}
	if total != 10000 {
		return Allocation{}, fmt.Errorf("%w: ratios total %d basis points", ErrInvalidSplit, total)
	}
	out := Allocation{Shares: make([]Share, len(parties))}
	for i, p := range parties {
		out.Shares[i].PartyID = p.ID
	}
	if mode == SplitAll {
		for i, amount := range distribute(electric, parties) {
			out.Shares[i].ElectricCents = amount
		}
	} else {
		out.ElectricExcludedCents = electric
	}
	for i, amount := range distribute(service, parties) {
		out.Shares[i].ServiceCents = amount
	}
	return out, nil
}

func distribute(amount Money, parties []Party) []Money {
	shares := make([]Money, len(parties))
	type remainder struct {
		index int
		value int64
	}
	remainders := make([]remainder, len(parties))
	var assigned Money
	for i, p := range parties {
		product := new(big.Int).Mul(big.NewInt(int64(amount)), big.NewInt(p.RatioBPS))
		quotient, rem := new(big.Int), new(big.Int)
		quotient.QuoRem(product, big.NewInt(10000), rem)
		shares[i] = Money(quotient.Int64())
		assigned += shares[i]
		remainders[i] = remainder{index: i, value: rem.Int64()}
	}
	sort.Slice(remainders, func(i, j int) bool {
		if remainders[i].value == remainders[j].value {
			return parties[remainders[i].index].ID < parties[remainders[j].index].ID
		}
		return remainders[i].value > remainders[j].value
	})
	for n := Money(0); n < amount-assigned; n++ {
		shares[remainders[int(n)].index]++
	}
	return shares
}
