package dc589

import "encoding/binary"

const (
	OnlineCardSwipe  byte = 0xB6
	OnlineCardDenied byte = 0xBD
	CardBalanceQuery byte = 0xD0
	CardBalanceReply byte = 0xD1
)

type CardSwipe struct {
	Port             byte
	CardNumber       uint32
	LocalDebitTenths byte // Hardware setting, never the platform's charge amount.
}

// The four reserved bytes carry no documented event sequence or remove-card
// indication. Each newly emitted B6 is a physical presentation, according to
// the configured firmware behavior; application retries use the stored UUID.
func ParseCardSwipe(f Frame) (CardSwipe, error) {
	if f.Command != OnlineCardSwipe || len(f.Data) != 10 || f.Data[0] == 0 {
		return CardSwipe{}, ErrPayload
	}
	n := binary.LittleEndian.Uint32(f.Data[1:5])
	if n == 0 {
		return CardSwipe{}, ErrPayload
	}
	return CardSwipe{Port: f.Data[0], CardNumber: n, LocalDebitTenths: f.Data[5]}, nil
}

func ParseCardBalanceQuery(f Frame) (uint32, error) {
	if f.Command != CardBalanceQuery || len(f.Data) != 5 || f.Data[0] != 0 {
		return 0, ErrPayload
	}
	n := binary.LittleEndian.Uint32(f.Data[1:])
	return n, nil
}

func BuildCardBalanceReply(session [6]byte, card uint32, valid bool, units uint16) (Frame, error) {
	if card == 0 && valid {
		return Frame{}, ErrPayload
	}
	d := make([]byte, 7)
	if !valid {
		d[0] = 1
		units = 0
	}
	binary.LittleEndian.PutUint32(d[1:5], card)
	binary.LittleEndian.PutUint16(d[5:7], units)
	return Frame{Command: CardBalanceReply, Session: session, Data: d}, nil
}

func BuildCardDenied(session [6]byte, card uint32, invalid bool, units uint16) (Frame, error) {
	if card == 0 {
		return Frame{}, ErrPayload
	}
	d := make([]byte, 9)
	if invalid {
		d[0] = 1
		units = 0
	}
	binary.LittleEndian.PutUint32(d[1:5], card)
	binary.LittleEndian.PutUint16(d[5:7], units)
	return Frame{Command: OnlineCardDenied, Session: session, Data: d}, nil
}

// A wire balance is an informational floor to 0.1 yuan, capped to uint16.
// Actual debits/refunds always retain integer cents in the wallet transaction.
func CardBalanceUnits(cents int64) uint16 {
	if cents <= 0 {
		return 0
	}
	if cents/10 > 65535 {
		return 65535
	}
	return uint16(cents / 10)
}
