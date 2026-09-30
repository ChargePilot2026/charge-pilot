package dc589sim

import (
	"encoding/binary"
	"fmt"
	wire "github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol/dc589"
)

// UpgradeModule models the PDF's board-to-modem F0-F5 exchange, on the
// internal link. These frames must never be sent to the operating gateway.
// Firmware byte transport is absent from the PDF and is outside this contract.
type UpgradeModule struct {
	Server string       `json:"server"`
	Stage  string       `json:"stage"`
	Frames []wire.Frame `json:"frames"`
}

func (m *UpgradeModule) Exchange(f wire.Frame) ([]wire.Frame, error) {
	m.Frames = append(m.Frames, f)
	var response []wire.Frame
	switch f.Command {
	case 0xf0:
		if len(f.Data) != 12 {
			return nil, fmt.Errorf("invalid F0")
		}
		n := func(i int) uint16 { return binary.LittleEndian.Uint16(f.Data[i : i+2]) }
		if n(0) > 255 || n(2) > 255 || n(4) > 255 || n(6) > 255 || n(8) == 0 {
			return nil, fmt.Errorf("invalid upgrade address")
		}
		m.Server = fmt.Sprintf("%d.%d.%d.%d:%d", n(0), n(2), n(4), n(6), n(8))
		m.Stage = "connecting"
		response = []wire.Frame{{Command: 0xf1, Data: []byte{0}}}
	case 0xf1:
		if len(f.Data) != 1 || f.Data[0] != 0 {
			return nil, fmt.Errorf("invalid F1")
		}
	case 0xf2:
		if len(f.Data) != 16 || f.Data[0] > 2 {
			return nil, fmt.Errorf("invalid F2")
		}
		m.Stage = []string{"connecting", "failed", "connected"}[f.Data[0]]
		response = []wire.Frame{{Command: 0xf3, Data: []byte{0}}}
	case 0xf3:
		if len(f.Data) != 1 || f.Data[0] != 0 {
			return nil, fmt.Errorf("invalid F3")
		}
	case 0xf4:
		if len(f.Data) != 2 || f.Data[0] != 0 || m.Stage != "connected" {
			return nil, fmt.Errorf("invalid F4 or module not connected")
		}
		m.Stage = "completed"
		response = []wire.Frame{{Command: 0xf5, Data: []byte{0}}}
	case 0xf5:
		if len(f.Data) != 1 || f.Data[0] != 0 {
			return nil, fmt.Errorf("invalid F5")
		}
	default:
		return nil, fmt.Errorf("unknown module command")
	}
	m.Frames = append(m.Frames, response...)
	return response, nil
}
func (m *UpgradeModule) Simulate() error {
	m.Frames = nil
	address := []byte{127, 0, 0, 0, 0, 0, 1, 0, 0xc6, 0x1e, 0, 0}
	if _, err := m.Exchange(wire.Frame{Command: 0xf0, Data: address}); err != nil {
		return err
	}
	progress := make([]byte, 16)
	progress[0] = 2
	copy(progress[1:], "123456789012345")
	if _, err := m.Exchange(wire.Frame{Command: 0xf2, Data: progress}); err != nil {
		return err
	}
	_, err := m.Exchange(wire.Frame{Command: 0xf4, Data: []byte{0, 0}})
	return err
}
