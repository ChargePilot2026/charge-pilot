package admin

import (
	"math"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/pricing"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
)

type previewSegment struct {
	Minutes int `json:"minutes"`
	Watts   int `json:"watts"`
}
type previewScenario struct {
	Name        string           `json:"name"`
	StartMinute int              `json:"start_minute"`
	Channel     pricing.Channel  `json:"channel"`
	Segments    []previewSegment `json:"segments"`
}
type pricingPreviewInput struct {
	Spec      pricing.Spec      `json:"spec"`
	Scenarios []previewScenario `json:"scenarios"`
}
type previewResult struct {
	previewScenario
	Fee               pricing.Fee `json:"fee"`
	BaseFee           pricing.Fee `json:"base_fee"`
	BeforeMinimum     pricing.Fee `json:"before_minimum"`
	EnergyWh          uint64      `json:"energy_wh"`
	Minutes           int         `json:"minutes"`
	CapReached        bool        `json:"cap_reached"`
	CardLimitExceeded bool        `json:"card_limit_exceeded"`
}

// Draft-only: no database writes or device commands. Amounts use the settlement engine.
func (a ResourceAPI) previewPricingTemplate(c *gin.Context) {
	var in pricingPreviewInput
	if !decodeResource(c, &in) {
		return
	}
	if pricing.ValidateTemplateSpec(in.Spec) != nil || !in.Spec.Mode.ServerBilled() || len(in.Scenarios) == 0 || len(in.Scenarios) > 450 {
		httpapi.BadRequest(c, "请提供有效的服务端计费方案和预览场景")
		return
	}
	results := make([]previewResult, 0, len(in.Scenarios))
	for _, scenario := range in.Scenarios {
		result, ok := calculatePricingPreview(in.Spec, scenario)
		if !ok {
			httpapi.BadRequest(c, "预览场景无效：开始时间、功率或时长超出范围")
			return
		}
		results = append(results, result)
	}
	httpapi.OK(c, gin.H{"items": results})
}

func calculatePricingPreview(spec pricing.Spec, scenario previewScenario) (previewResult, bool) {
	result := previewResult{previewScenario: scenario}
	if scenario.StartMinute < 0 || scenario.StartMinute >= 1440 || len(scenario.Segments) == 0 || len(scenario.Segments) > 48 || len([]rune(scenario.Name)) > 128 {
		return result, false
	}
	if scenario.Channel != pricing.ChannelDefault && scenario.Channel != pricing.ChannelTemp && scenario.Channel != pricing.ChannelCard {
		return result, false
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600)).Add(time.Duration(scenario.StartMinute) * time.Minute)
	usage := pricing.Usage{Start: start, Channel: scenario.Channel}
	at := start
	for _, segment := range scenario.Segments {
		if segment.Minutes < 1 || segment.Minutes > 1440 || segment.Watts < 0 || segment.Watts > 100000 {
			return result, false
		}
		result.Minutes += segment.Minutes
		if result.Minutes > 1440 {
			return result, false
		}
		end := at.Add(time.Duration(segment.Minutes) * time.Minute)
		wh := uint64(math.Round(float64(segment.Watts) * float64(segment.Minutes) / 60))
		usage.Samples = append(usage.Samples, pricing.Sample{Start: at, End: end, EnergyWh: wh, PowerW: uint32(segment.Watts)})
		usage.EnergyWh += wh
		at = end
	}
	usage.End = at
	fee, err := pricing.Cost(spec, usage)
	if err != nil {
		return result, false
	}
	base := spec
	base.Multiplier = nil
	base.MinElectricCents = 0
	base.FreeMinutes = 0
	baseFee, err := pricing.Cost(base, usage)
	if err != nil {
		return result, false
	}
	beforeMinimum := spec
	beforeMinimum.MinElectricCents = 0
	unflooredFee, err := pricing.Cost(beforeMinimum, usage)
	if err != nil {
		return result, false
	}
	result.BeforeMinimum = unflooredFee
	result.Fee, result.BaseFee, result.EnergyWh = fee, baseFee, usage.EnergyWh
	result.CapReached = spec.SpendCapCents > 0 && fee.TotalCents >= spec.SpendCapCents
	result.CardLimitExceeded = scenario.Channel == pricing.ChannelCard && spec.CardMaxMinutes > 0 && result.Minutes > int(spec.CardMaxMinutes)
	return result, true
}
