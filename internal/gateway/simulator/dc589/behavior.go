package dc589sim

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	wire "github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol/dc589"
)

// Input changes physical conditions or presents a card; no gateway/DB shortcut.
type Input struct {
	Config      *wire.ConfigTable `json:"config,omitempty"`
	Type        string            `json:"type"`
	Port        byte              `json:"port"`
	Card        uint32            `json:"card"`
	Power       uint32            `json:"power_deciwatts"`
	Temperature int16             `json:"temperature_c"`
	Voltage     uint16            `json:"voltage_v"`
	Code        byte              `json:"code"`
	Consumer    byte              `json:"consumer"`
	Quantity    uint16            `json:"quantity"`
	Reply       chan Result       `json:"-"`
}
type Result struct {
	State *Snapshot `json:"state,omitempty"`
	Error string    `json:"error,omitempty"`
}
type PhysicalPort struct {
	Connected bool   `json:"connected"`
	Power     uint32 `json:"power_deciwatts"`
	Fault     byte   `json:"fault"`
	Card      uint32 `json:"present_card"`
}
type Snapshot struct {
	Online      bool                   `json:"online"`
	Smoke       bool                   `json:"smoke"`
	Completed   map[string]bool        `json:"completed_orders"`
	SavedAt     time.Time              `json:"saved_at"`
	Cards       map[uint32]CardStatus  `json:"cards"`
	Module      UpgradeModule          `json:"upgrade_module"`
	Identity    wire.DeviceIdentity    `json:"identity"`
	Config      wire.ConfigTable       `json:"config"`
	RawConfig   []byte                 `json:"raw_config"`
	RemovePower uint16                 `json:"remove_power_deciwatts"`
	Temperature int16                  `json:"temperature_c"`
	Voltage     uint16                 `json:"voltage_v"`
	Ports       map[byte]*PhysicalPort `json:"ports"`
	Charging    []ChargeState          `json:"charging"`
	Pending     []wire.Frame           `json:"pending_reports"`
}
type CardStatus struct {
	Valid        bool   `json:"valid"`
	BalanceUnits uint16 `json:"balance_tenths"`
	Denied       bool   `json:"denied"`
}
type ChargeState struct {
	Port           byte
	Order          [8]byte
	Mode           wire.ChargeMode
	Started        time.Time
	Energy         uint64
	Target         uint32
	Remaining      time.Duration
	Consumer       byte
	Card           uint32
	Band           byte
	Peak           uint32
	FloatSeconds   uint32
	RemovedSeconds uint32
}

// ControlHandler is intended for a loopback listener. All inputs are serialized
// with wire commands and metering in the board loop; tests use the same path.
func ControlHandler(ctx context.Context, inputs chan<- Input) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		in := Input{Type: "state"}
		if r.Method == http.MethodPost && r.URL.Path == "/events" {
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&in); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		} else if r.Method != http.MethodGet || r.URL.Path != "/state" {
			http.NotFound(w, r)
			return
		}
		in.Reply = make(chan Result, 1)
		timeout, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		select {
		case inputs <- in:
		case <-timeout.Done():
			http.Error(w, "device unavailable", 503)
			return
		case <-ctx.Done():
			http.Error(w, "simulator stopped", 503)
			return
		}
		select {
		case out := <-in.Reply:
			if out.Error != "" {
				w.WriteHeader(400)
			}
			_ = json.NewEncoder(w).Encode(out)
		case <-timeout.Done():
			http.Error(w, "device unavailable", 503)
		case <-ctx.Done():
			http.Error(w, "simulator stopped", 503)
		}
	})
}

func (b *board) physical(port byte) *PhysicalPort {
	if p := b.ports[port]; p != nil {
		return p
	}
	p := &PhysicalPort{Connected: true, Power: b.config.PowerDeciWatts}
	b.ports[port] = p
	return p
}
func (b *board) power(port byte) uint32 {
	p := b.physical(port)
	if !p.Connected || p.Fault != 0 {
		return 0
	}
	return p.Power
}
func (b *board) portState(port byte) byte {
	if port == 0 || int(port) > b.config.PortCount {
		return 0xff
	}
	if p := b.physical(port); p.Fault != 0 {
		return p.Fault
	}
	if b.charging[port] != nil {
		return 1
	}
	return 0
}
func (b *board) telemetry(c *charge) protocol.PortTelemetry {
	return protocol.PortTelemetry{Port: c.port, RemainingSecs: b.remainingSecs(c), ChargedSeconds: uint32(b.now().Sub(c.startedAt).Seconds()), RemainingMWh: c.remainingMilliWh(), ChargedMWh: c.chargedMilliWh(), PowerDeciWatts: b.power(c.port)}
}
func (b *board) sendAllPorts(short bool, command byte) error {
	states := make([]byte, b.config.PortCount)
	ports := make([]protocol.PortTelemetry, 0, len(b.charging))
	for i := range states {
		states[i] = b.portState(byte(i + 1))
		if c := b.charging[byte(i+1)]; c != nil {
			ports = append(ports, b.telemetry(c))
		}
	}
	// The real 20-port board emits at most ten charging blocks per frame.
	count := len(ports)
	if short {
		count = 0
		ports = nil
	}
	for from := 0; ; from += 10 {
		end := min(from+10, len(ports))
		f, err := wire.BuildHeartbeat(b.config.Identity, &wire.PortStatus{DeviceStatus: b.deviceStatus(), VoltageV: b.voltage, TemperatureC: b.temperature, PortStates: states, Charging: ports[from:end]})
		if err != nil {
			return err
		}
		if command == 0xB1 {
			f.Data = f.Data[17:]
			if short {
				f.Data = f.Data[:5+len(states)]
			} else {
				f.Data[5+len(states)] = byte(count)
			}
		} else {
			f.Data[22+len(states)] = byte(count)
		}
		f.Command = command
		if err = b.writer.send(f); err != nil {
			return err
		}
		if end == len(ports) {
			return nil
		}
	}
}
func (b *board) deviceStatus() byte {
	if b.smoke {
		return 2
	}
	if b.configTable.TemperatureGuard != 0xff && b.temperature >= int16(b.configTable.TemperatureGuard) {
		return 1
	}
	return 0
}
func (b *board) portReport(port, command byte) wire.Frame {
	d := make([]byte, 36)
	d[0], d[1] = port, b.portState(port)
	if c := b.charging[port]; c != nil {
		copy(d[2:10], c.orderBCD[:])
		encodeCivil(d[10:16], c.startedAt)
		left := b.remainingSecs(c)
		binary.LittleEndian.PutUint16(d[16:18], uint16(left/60))
		d[18] = byte(left % 60)
		d[19] = byte(c.mode)
		used := uint32(b.now().Sub(c.startedAt).Seconds())
		binary.LittleEndian.PutUint16(d[20:22], uint16(used/60))
		d[22] = byte(used % 60)
		d[23] = c.consumer
		binary.LittleEndian.PutUint16(d[24:26], uint16(c.remainingMilliWh()/1000))
		binary.LittleEndian.PutUint16(d[26:28], uint16(c.chargedMilliWh()/1000))
		binary.LittleEndian.PutUint16(d[28:30], uint16(b.power(port)))
		if c.band > 0 {
			d[30] = c.band - 1
		}
		d[31] = byte(b.configTable.CardAmountCents / 10)
		binary.LittleEndian.PutUint32(d[32:36], c.card)
	}
	return wire.Frame{Command: command, Data: d}
}
func encodeCivil(out []byte, at time.Time) {
	at = wire.Civil(at)
	v := []int{at.Year() % 100, int(at.Month()), at.Day(), at.Hour(), at.Minute(), at.Second()}
	for i, n := range v {
		out[i] = byte(n/10<<4 | n%10)
	}
}

func (b *board) powerControl(f wire.Frame) error {
	d := append([]byte(nil), f.Data...)
	if len(d) != 6 {
		return fmt.Errorf("invalid E0 length")
	}
	value := binary.LittleEndian.Uint16(d[2:4])
	valid := d[0] <= 1 && d[1] == 0 && d[4] == 0 && d[5] == 0
	if !valid || (d[0] == 1 && value == 0xfff1) {
		d[0] = 2
		binary.LittleEndian.PutUint16(d[2:4], 0xfff1)
	} else if d[0] == 1 {
		b.removePower = value
		if err := b.persist(); err != nil {
			return err
		}
	} else {
		binary.LittleEndian.PutUint16(d[2:4], b.removePower)
	}
	return b.writer.send(wire.Frame{Command: 0xE1, Data: d})
}

func configCode(f wire.Frame) byte {
	d := f.Data
	if len(d) != 33 {
		return 1
	}
	n := func(i int) uint16 { return binary.LittleEndian.Uint16(d[i : i+2]) }
	switch {
	case d[0] > 4:
		return 1
	case d[1] > 4:
		return 2
	case n(2) > 999:
		return 3
	case n(4) > 999:
		return 4
	case d[6] > 250:
		return 5
	case d[7] > 1:
		return 6
	}
	previous := uint16(0)
	for i := 0; i < 5; i++ {
		v := n(8 + 2*i)
		if v > 9990 || v > 0 && v <= previous {
			return 7
		}
		if v > 0 {
			previous = v
		}
		if (i == 0 && d[18] != 100) || v > 0 && (d[18+i] == 0 || d[18+i] > 100) {
			return 8
		}
	}
	switch {
	case d[23] > 1:
		return 9
	case n(24) < 10 || n(24) > 500:
		return 10
	case n(26) < 120 || n(26) > 10800:
		return 11
	case n(28) < 5 || n(28) > 3600:
		return 12
	case d[32] != 255 && (d[32] < 50 || d[32] > 100):
		return 13
	}
	return 0
}

func (b *board) input(in Input) Result {
	err := b.applyInput(in)
	if err != nil {
		return Result{Error: err.Error()}
	}
	state := b.snapshot()
	if err = b.persist(); err != nil {
		return Result{Error: err.Error()}
	}
	return Result{State: &state}
}
func (b *board) applyInput(in Input) error {
	if in.Type == "state" {
		return nil
	}
	if in.Type == "config" {
		if in.Config == nil {
			return fmt.Errorf("config required")
		}
		f, err := wire.BuildSetConfig(*in.Config)
		if err != nil {
			return err
		}
		if code := configCode(f); code != 0 {
			return fmt.Errorf("configuration rejected: C4 code %d", code)
		}
		b.configTable = *in.Config
		b.rawConfig = f.Data
		return nil
	}
	if in.Type == "request-time" {
		return b.writer.send(wire.BuildTimeRequest())
	}
	if in.Type == "heartbeat" {
		return b.sendAllPorts(false, wire.Heartbeat)
	}
	if in.Type == "restart" || in.Type == "upgrade" {
		if !b.online {
			return fmt.Errorf("device is disconnected")
		}
		code := byte(1)
		if in.Type == "upgrade" {
			code = 2
		}
		d := make([]byte, 10)
		d[0] = code
		return b.remote(wire.Frame{Command: 0xA2, Data: d})
	}
	if in.Type == "balance" {
		if in.Card == 0 {
			return fmt.Errorf("card must be nonzero")
		}
		d := make([]byte, 5)
		binary.LittleEndian.PutUint32(d[1:], in.Card)
		return b.writer.send(wire.Frame{Command: wire.CardBalanceQuery, Data: d})
	}
	if in.Type == "request-config" {
		return b.writer.send(wire.Frame{Command: 0xC7, Data: []byte{0}})
	}
	if in.Type == "temperature" {
		if in.Temperature < -50 || in.Temperature > 205 {
			return fmt.Errorf("temperature out of range")
		}
		b.temperature = in.Temperature
		if b.deviceStatus() == 1 {
			return b.sendFault(0xff, 0xaa)
		}
		return nil
	}
	if in.Type == "voltage" {
		b.voltage = in.Voltage
		return nil
	}
	if in.Type == "smoke" {
		b.smoke = in.Code != 0
		if b.smoke {
			return b.sendFault(0xff, 0xbb)
		}
		return nil
	}
	if in.Type == "disconnect" {
		if !b.online {
			return fmt.Errorf("device is already disconnected")
		}
		return b.conn.Close()
	}
	if in.Port == 0 || int(in.Port) > b.config.PortCount {
		return fmt.Errorf("port out of range")
	}
	p := b.physical(in.Port)
	switch in.Type {
	case "power":
		if in.Power > 65535 {
			return fmt.Errorf("power out of wire range")
		}
		p.Power = in.Power
		return nil
	case "plug":
		p.Connected = true
		return nil
	case "unplug":
		p.Connected = false
		return nil
	case "fault":
		if in.Code != 0 && in.Code != 3 && in.Code != 4 {
			return fmt.Errorf("port fault must be 0,3,4")
		}
		p.Fault = in.Code
		if in.Code == 0 {
			return nil
		}
		return b.sendFault(in.Port, in.Code)
	case "remove-card":
		p.Card = 0
		return nil
	case "card":
		if in.Card == 0 {
			return fmt.Errorf("card must be nonzero")
		}
		if p.Card == in.Card {
			return nil
		}
		if p.Card != 0 {
			return fmt.Errorf("remove the current card first")
		}
		p.Card = in.Card
		d := make([]byte, 10)
		d[0] = in.Port
		binary.LittleEndian.PutUint32(d[1:5], in.Card)
		d[5] = byte(b.configTable.CardAmountCents / 10)
		return b.writer.send(wire.Frame{Command: wire.OnlineCardSwipe, Data: d})
	case "balance":
		if in.Card == 0 {
			return fmt.Errorf("card must be nonzero")
		}
		d := make([]byte, 5)
		binary.LittleEndian.PutUint32(d[1:], in.Card)
		return b.writer.send(wire.Frame{Command: wire.CardBalanceQuery, Data: d})
	case "local-start":
		if in.Consumer != 0 && in.Consumer != 1 && in.Consumer != 4 {
			return fmt.Errorf("local consumer must be 0,1,4")
		}
		if b.charging[in.Port] != nil || p.Fault != 0 {
			return fmt.Errorf("port busy or faulty")
		}
		quantity := in.Quantity
		if quantity == 0 {
			quantity = b.configTable.LocalCoinTime
			if in.Consumer == 1 {
				quantity = b.configTable.LocalCardTime
			}
		}
		c := &charge{port: in.Port, startedAt: b.now(), mode: wire.ByTime, remaining: time.Duration(quantity) * time.Minute, consumer: in.Consumer, card: in.Card, band: 1}
		if b.configTable.RunMode == 3 || b.configTable.RunMode == 4 {
			c.mode = wire.ByEnergy
			c.targetMilliWh = uint32(quantity) * 10000
		}
		if quantity == 0 {
			return fmt.Errorf("local quantity must be nonzero")
		}
		b.charging[in.Port] = c
		return b.queueReport(b.portReport(in.Port, 0xB4))
	default:
		return fmt.Errorf("unknown physical event %q", in.Type)
	}
}
func (b *board) sendFault(port, code byte) error {
	d := []byte{port, code, byte(b.temperature + 50), 0, 0}
	binary.LittleEndian.PutUint16(d[3:5], b.voltage)
	return b.queueReport(wire.Frame{Command: wire.Fault, Data: d})
}
func (b *board) queueReport(f wire.Frame) error {
	b.pending = append(b.pending, f)
	if err := b.persist(); err != nil {
		return err
	}
	return b.writer.send(f)
}
func (b *board) acknowledge(command, port byte) {
	for i, f := range b.pending {
		p := byte(0)
		if len(f.Data) > 0 {
			p = f.Data[0]
		}
		if f.Command == wire.ChargeEnd && len(f.Data) > 1 {
			p = f.Data[1]
		}
		if f.Command == command && p == port {
			b.pending = append(b.pending[:i], b.pending[i+1:]...)
			break
		}
	}
	_ = b.persist()
}
func (b *board) snapshot() Snapshot {
	s := Snapshot{Identity: b.config.Identity, Config: b.configTable, RawConfig: b.rawConfig, RemovePower: b.removePower, Temperature: b.temperature, Voltage: b.voltage, Ports: map[byte]*PhysicalPort{}, Pending: append([]wire.Frame(nil), b.pending...)}
	s.Online, s.Smoke = b.online, b.smoke
	s.Completed = map[string]bool{}
	for k, v := range b.completed {
		s.Completed[k] = v
	}
	s.SavedAt = time.Now()
	s.Cards = map[uint32]CardStatus{}
	for n, v := range b.cards {
		s.Cards[n] = v
	}
	s.Module = b.module
	for i := 1; i <= b.config.PortCount; i++ {
		v := *b.physical(byte(i))
		s.Ports[byte(i)] = &v
	}
	for _, c := range b.charging {
		s.Charging = append(s.Charging, ChargeState{c.port, c.orderBCD, c.mode, c.startedAt, c.wattDeciSeconds, c.targetMilliWh, c.remaining, c.consumer, c.card, c.band, c.peak, c.floatSeconds, c.removedSeconds})
	}
	sort.Slice(s.Charging, func(i, j int) bool { return s.Charging[i].Port < s.Charging[j].Port })
	return s
}
func (b *board) persist() error {
	if b.config.StateFile == "" {
		return nil
	}
	raw, err := json.MarshalIndent(b.snapshot(), "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(b.config.StateFile+".tmp", raw, 0600); err != nil {
		return err
	}
	return os.Rename(b.config.StateFile+".tmp", b.config.StateFile)
}
func (b *board) restore() error {
	if b.config.StateFile == "" {
		return nil
	}
	raw, err := os.ReadFile(b.config.StateFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var s Snapshot
	if err = json.Unmarshal(raw, &s); err != nil {
		return err
	}
	if s.Identity.BoardID != b.config.Identity.BoardID {
		return fmt.Errorf("state belongs to another board")
	}
	if len(s.Ports) != b.config.PortCount {
		return fmt.Errorf("saved port count differs; use a new state file")
	}
	b.config.Identity = s.Identity
	b.smoke = s.Smoke
	if s.Completed != nil {
		b.completed = s.Completed
	}
	if err = s.Config.Validate(); err != nil {
		return err
	}
	b.configTable = s.Config
	b.rawConfig = s.RawConfig
	b.removePower = s.RemovePower
	b.temperature = s.Temperature
	b.voltage = s.Voltage
	b.ports = s.Ports
	b.pending = s.Pending
	b.cards = s.Cards
	if b.cards == nil {
		b.cards = map[uint32]CardStatus{}
	}
	b.module = s.Module
	if b.ports == nil {
		b.ports = map[byte]*PhysicalPort{}
	}
	b.lastMeter = s.SavedAt
	for _, v := range s.Charging {
		if v.Port == 0 || int(v.Port) > b.config.PortCount {
			return fmt.Errorf("saved port out of range")
		}
		b.charging[v.Port] = &charge{port: v.Port, orderBCD: v.Order, mode: v.Mode, startedAt: v.Started, wattDeciSeconds: v.Energy, targetMilliWh: v.Target, remaining: v.Remaining, consumer: v.Consumer, card: v.Card, band: v.Band, peak: v.Peak, floatSeconds: v.FloatSeconds, removedSeconds: v.RemovedSeconds}
	}
	return nil
}

func (b *board) remote(f wire.Frame) error {
	if len(f.Data) != 10 || f.Data[0] < 1 || f.Data[0] > 3 || f.Data[1] > 1 {
		return nil
	}
	if f.Data[0] != 1 && f.Data[1] == 1 {
		for _, c := range f.Data[2:10] {
			if c < 32 || c > 126 {
				return nil
			}
		}
		b.config.Identity.SoftwareID = string(f.Data[2:10])
	}
	// The PDF states successful reset/upgrade does not emit A3. Upgrade binary
	// transport is not specified; simulate flashing and the F0-F5 module exchange
	// locally, then reconnect and expose the new identity in A0/A4.
	if f.Data[0] != 1 {
		if err := b.module.Simulate(); err != nil {
			return err
		}
		b.config.Log.Printf("module upgrade exchange F0/F1/F2/F3/F4/F5 completed locally")
		b.config.Identity.SoftwareVersion++
	}
	for port, c := range b.charging {
		delete(b.charging, port)
		if err := b.reportEnd(c, 3); err != nil {
			return err
		}
	}
	if err := b.persist(); err != nil {
		return err
	}
	return b.conn.Close()
}

func (b *board) updateBand(c *charge) error {
	// Platform billing and long charging bypass discounts and full/remove checks.
	if c.mode == wire.PlatformBilling || c.mode == wire.LongPlatformBilling || c.mode == wire.LongTime || c.mode == wire.LongEnergy {
		return nil
	}
	band := byte(1)
	for i, watts := range b.configTable.TierWatts {
		if watts == 0 {
			break
		}
		band = byte(i + 1)
		if c.peak <= uint32(watts)*10 {
			break
		}
	}
	if band <= c.band {
		return nil
	}
	old := c.band
	if old == 0 {
		old = 1
	}
	before := b.remainingSecs(c)
	previous := b.configTable.TierRatioPercent[old-1]
	next := b.configTable.TierRatioPercent[band-1]
	if previous == 0 {
		previous = 100
	}
	if next == 0 {
		next = 100
	}
	if !energyBilled(c.mode) {
		c.remaining = time.Duration(int64(c.remaining) * int64(next) / int64(previous))
	} else {
		c.targetMilliWh = c.chargedMilliWh() + uint32(uint64(c.remainingMilliWh())*uint64(next)/uint64(previous))
	}
	c.band = band
	f, err := wire.BuildChargingBand(wire.ChargingBandReport{Port: c.port, BandBefore: old, BandAfter: band, MinutesBefore: uint16(before / 60), MinutesAfter: uint16(b.remainingSecs(c) / 60), PowerDeciWatts: uint16(b.power(c.port))})
	if err != nil {
		return err
	}
	return b.writer.send(f)
}
