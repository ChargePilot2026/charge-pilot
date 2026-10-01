package pricing

import (
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol"
	"time"
)

// C2 没有累计电表，只有完整的心跳测量值可用。
// 设备侧的已用秒数把每条读数定位到 BB 的设备时钟上；
// 接收时间只用来剔除过期或前后矛盾的上报，
// 绝不用来分摊 Wh。
func MeasuredSegments(start time.Time, end protocol.Event, samples []protocol.Event) []MeterSegment {
	if start.IsZero() || end.StartedAt.IsZero() || end.EndedAt.Before(start) || end.EndedAt.Sub(start) > 7*24*time.Hour || end.EndedAt.Sub(end.StartedAt) != time.Duration(end.ChargedSeconds)*time.Second || len(samples) > 10080 {
		return nil
	}
	delta := start.Sub(end.StartedAt)
	if delta < -5*time.Second || delta > 5*time.Second {
		return nil
	}
	cursor, previous := start, uint32(0)
	var previousPower *uint32
	segments := []MeterSegment{}
	for _, event := range samples {
		if event.Type != protocol.Heartbeat || event.DeviceID != end.DeviceID {
			continue
		}
		for _, port := range event.ChargingPorts {
			if port.Port != end.Port {
				continue
			}
			at := end.StartedAt.Add(time.Duration(port.ChargedSeconds) * time.Second)
			lag := event.ReceivedAt.Sub(at)
			if lag < -5*time.Second || lag > 5*time.Second || port.ChargedSeconds > end.ChargedSeconds || port.ChargedMWh%1000 != 0 {
				return nil
			}
			wh := port.ChargedMWh / 1000
			if wh < previous || wh > end.EnergyMilliKWh || at.Before(cursor) {
				return nil
			}
			if at.Equal(cursor) {
				if wh != previous {
					return nil
				}
				power := (port.PowerDeciWatts + 5) / 10
				previousPower = &power
				continue
			}
			segment := MeterSegment{StartedAt: cursor, EndedAt: at, EnergyWh: wh - previous}
			power := (port.PowerDeciWatts + 5) / 10
			// A previous reading may describe the interval only for a verified
			// consecutive heartbeat gap of at most 60 seconds. Larger gaps stay
			// explicitly missing instead of inferring power from energy.
			if previousPower != nil && at.Sub(cursor) <= 60*time.Second {
				v := *previousPower
				segment.PowerW = &v
				segment.PeakW = v
				if power > segment.PeakW {
					segment.PeakW = power
				}
			}
			segments = append(segments, segment)
			previousPower = &power
			cursor, previous = at, wh
		}
	}
	if len(segments) == 0 {
		return nil
	}
	if cursor.Equal(end.EndedAt) {
		if previous != end.EnergyMilliKWh {
			return nil
		}
	} else {
		segment := MeterSegment{StartedAt: cursor, EndedAt: end.EndedAt, EnergyWh: end.EnergyMilliKWh - previous}
		if previousPower != nil && end.EndedAt.Sub(cursor) <= 60*time.Second {
			v := *previousPower
			segment.PowerW = &v
			segment.PeakW = v
		}
		segments = append(segments, segment)
	}
	return segments
}
