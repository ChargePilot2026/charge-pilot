package dc589sim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// RunTUI owns only presentation. Every operation enters the same serialized
// board event loop as HTTP controls and real gateway commands.
func RunTUI(ctx context.Context, config Config, inputs chan Input, cancel context.CancelFunc) error {
	logs := &terminalLog{}
	config.Log = log.New(logs, "", log.Ltime)
	done := make(chan error, 1)
	go func() { done <- Run(ctx, config) }()
	defer cancel()
	m := terminalModel{ctx: ctx, inputs: inputs, logs: logs, port: 1, width: 100, height: 35, notice: "等待网关注册；设备须先在后台创建", state: Snapshot{Identity: config.Identity, Ports: map[byte]*PhysicalPort{}}}
	program := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	go func() { err := <-done; program.Send(boardDone{err}) }()
	_, err := program.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}

type terminalLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *terminalLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSpace(string(p)))
	if len(l.lines) > 200 {
		l.lines = l.lines[len(l.lines)-200:]
	}
	return len(p), nil
}
func (l *terminalLog) tail(n int) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines[max(0, len(l.lines)-n):], "\n")
}

type stateMsg Result
type boardDone struct{ err error }
type refreshMsg struct{}
type field struct{ label, value string }
type action struct {
	label, kind string
	fields      []field
}

var terminalActions = []action{
	{"在线刷卡（移开后才能重刷）", "card", []field{{"卡号（十进制）", "100001"}}},
	{"移开卡片", "remove-card", nil},
	{"查询卡余额", "balance", []field{{"卡号（十进制）", "100001"}}},
	{"调整负载功率（充电中可用）", "power", []field{{"功率（W）", "150"}}},
	{"插入充电器", "plug", nil}, {"拔出充电器", "unplug", nil},
	{"本地投币 / 离线卡 / 免费启动", "local-start", []field{{"消费方式：0投币 / 1离线卡 / 4免费", "0"}, {"数量：时间模式为分钟，电量模式为0.01度", "60"}, {"离线卡号（无卡填0）", "0"}}},
	{"端口故障 / 恢复", "fault", []field{{"故障：0恢复 / 3故障 / 4过载", "3"}}},
	{"设置温度", "temperature", []field{{"温度（℃，-50..205）", "25"}}},
	{"设置电压", "voltage", []field{{"电压（V）", "220"}}},
	{"烟雾告警 / 恢复", "smoke", []field{{"0恢复 / 1烟雾", "1"}}},
	{"断开网络并自动重连", "disconnect", nil},
	{"设备重启（终止当前充电）", "restart", nil},
	{"模拟升级与模块F0-F5交换", "upgrade", nil},
	{"立即上报心跳", "heartbeat", nil}, {"请求服务器校时", "request-time", nil},
	{"请求参数同步（C7，旧固件行为）", "request-config", nil},
	{"编辑设备本地参数表", "config", nil},
}

type terminalModel struct {
	ctx                                   context.Context
	inputs                                chan Input
	logs                                  *terminalLog
	state                                 Snapshot
	port                                  byte
	tab, selection, width, height, scroll int
	menu, editing                         bool
	current                               action
	fields                                []field
	focus                                 int
	notice                                string
}

func (m terminalModel) Init() tea.Cmd { return m.send(Input{Type: "state"}) }
func (m terminalModel) send(in Input) tea.Cmd {
	return func() tea.Msg {
		in.Reply = make(chan Result, 1)
		ctx, cancel := context.WithTimeout(m.ctx, 7*time.Second)
		defer cancel()
		select {
		case m.inputs <- in:
		case <-ctx.Done():
			return stateMsg{Error: "设备暂时无法响应"}
		}
		select {
		case r := <-in.Reply:
			return stateMsg(r)
		case <-ctx.Done():
			return stateMsg{Error: "等待设备注册或重连…"}
		}
	}
}
func refresh() tea.Cmd { return tea.Tick(time.Second, func(time.Time) tea.Msg { return refreshMsg{} }) }
func (m terminalModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case boardDone:
		if v.err != nil {
			m.notice = v.err.Error()
		}
		return m, tea.Quit
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
	case refreshMsg:
		return m, m.send(Input{Type: "state"})
	case stateMsg:
		if v.State != nil {
			if v.State.Online && !m.state.Online && m.notice == "等待网关注册；设备须先在后台创建" {
				m.notice = "设备已在线，可通过操作菜单模拟物理操作"
			}
			m.state = *v.State
			if int(m.port) > len(m.state.Ports) {
				m.port = 1
			}
		}
		if v.Error != "" {
			m.notice = v.Error
		}
		return m, refresh()
	case tea.KeyMsg:
		key := v.String()
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.editing {
			switch key {
			case "esc":
				m.editing = false
			case "tab", "down":
				if len(m.fields) > 0 {
					m.focus = (m.focus + 1) % len(m.fields)
				}
			case "shift+tab", "up":
				if len(m.fields) > 0 {
					m.focus = (m.focus + len(m.fields) - 1) % len(m.fields)
				}
			case "backspace", "ctrl+h":
				if len(m.fields) > 0 {
					r := []rune(m.fields[m.focus].value)
					if len(r) > 0 {
						m.fields[m.focus].value = string(r[:len(r)-1])
					}
				}
			case "ctrl+u":
				if len(m.fields) > 0 {
					m.fields[m.focus].value = ""
				}
			case "enter":
				in, err := m.event()
				if err != nil {
					m.notice = err.Error()
					return m, nil
				}
				m.editing = false
				m.menu = false
				m.notice = "已提交：" + m.current.label
				return m, m.send(in)
			default:
				if v.Type == tea.KeyRunes && len(m.fields) > 0 {
					m.fields[m.focus].value += string(v.Runes)
				}
			}
			return m, nil
		}
		if m.menu {
			switch key {
			case "esc":
				m.menu = false
			case "up", "k":
				m.selection = (m.selection + len(terminalActions) - 1) % len(terminalActions)
			case "down", "j":
				m.selection = (m.selection + 1) % len(terminalActions)
			case "enter":
				m.current = terminalActions[m.selection]
				m.fields = append([]field(nil), m.current.fields...)
				if m.current.kind == "power" {
					m.fields = m.powerFields()
				}
				if m.current.kind == "config" {
					m.fields = m.configFields()
				}
				m.focus = 0
				m.editing = true
			}
			return m, nil
		}
		switch key {
		case "q":
			return m, tea.Quit
		case "tab":
			m.tab = (m.tab + 1) % 4
			m.scroll = 0
		case "m", "enter":
			m.menu = true
		case "p":
			m.current = terminalActions[3]
			m.fields = m.powerFields()
			m.focus = 0
			m.editing = true
		case "up", "k":
			if m.port > 1 {
				m.port--
			}
		case "down", "j":
			if int(m.port) < len(m.state.Ports) {
				m.port++
			}
		case "pgdown":
			m.scroll += max(1, m.height-10)
		case "pgup":
			m.scroll = max(0, m.scroll-max(1, m.height-10))
		}
	}
	return m, nil
}
func (m terminalModel) powerFields() []field {
	value := "150"
	if port := m.state.Ports[m.port]; port != nil {
		value = strconv.FormatFloat(float64(port.Power)/10, 'f', 1, 64)
	}
	return []field{{"功率（W，充电中可调整）", value}}
}
func (m terminalModel) event() (Input, error) {
	in := Input{Type: m.current.kind, Port: m.port}
	number := func(i int, bits int) (uint64, error) {
		n, err := strconv.ParseUint(m.fields[i].value, 10, bits)
		if err != nil {
			return 0, fmt.Errorf("%s：请输入有效数字", m.fields[i].label)
		}
		return n, nil
	}
	switch in.Type {
	case "card", "balance":
		n, err := number(0, 32)
		if err != nil {
			return in, err
		}
		in.Card = uint32(n)
	case "power":
		n, err := strconv.ParseFloat(m.fields[0].value, 64)
		if err != nil || n < 0 || n > 6553.5 {
			return in, fmt.Errorf("功率范围为0..6553.5 W")
		}
		in.Power = uint32(n * 10)
	case "fault", "smoke":
		n, err := number(0, 8)
		if err != nil {
			return in, err
		}
		in.Code = byte(n)
	case "temperature":
		n, err := strconv.ParseInt(m.fields[0].value, 10, 16)
		if err != nil {
			return in, err
		}
		in.Temperature = int16(n)
	case "voltage":
		n, err := number(0, 16)
		if err != nil {
			return in, err
		}
		in.Voltage = uint16(n)
	case "local-start":
		n, err := number(0, 8)
		if err != nil {
			return in, err
		}
		in.Consumer = byte(n)
		n, err = number(1, 16)
		if err != nil {
			return in, err
		}
		in.Quantity = uint16(n)
		n, err = number(2, 32)
		if err != nil {
			return in, err
		}
		in.Card = uint32(n)
	case "config":
		c := m.state.Config
		values := make([]uint16, len(m.fields))
		for i := range values {
			n, err := number(i, 16)
			if err != nil {
				return in, err
			}
			values[i] = uint16(n)
		}
		for _, i := range []int{0, 4, 5, 10} {
			if values[i] > 255 {
				return in, fmt.Errorf("%s：数值超出字节范围", m.fields[i].label)
			}
		}
		c.RunMode = byte(values[0])
		c.LocalCoinTime = values[1]
		c.LocalCardTime = values[2]
		c.CardAmountCents = values[3]
		c.CardRefund = byte(values[4])
		c.StopWhenFull = byte(values[5])
		c.FloatDeciWatts = values[6]
		c.FloatSeconds = values[7]
		c.RemoveSeconds = values[8]
		c.TemperatureGuard = byte(values[10])
		for i := 0; i < 5; i++ {
			if values[16+i] > 100 {
				return in, fmt.Errorf("档位折扣不能超过100%%")
			}
			c.TierWatts[i] = values[11+i]
			c.TierRatioPercent[i] = byte(values[16+i])
		}
		in.Config = &c
	}
	return in, nil
}
func (m terminalModel) configFields() []field {
	c := m.state.Config
	labels := []string{"运行模式(0..4)", "投币数量(分钟/0.01度)", "离线卡数量(分钟/0.01度)", "刷卡金额(分，10的倍数)", "刷卡退费(0/1)", "满充停止(0/1)", "浮充功率(0.1W)", "浮充时间(秒)", "移除检测(秒)", "保留值(0)", "温度保护(50..100/255关闭)"}
	values := []uint16{uint16(c.RunMode), c.LocalCoinTime, c.LocalCardTime, c.CardAmountCents, uint16(c.CardRefund), uint16(c.StopWhenFull), c.FloatDeciWatts, c.FloatSeconds, c.RemoveSeconds, 0, uint16(c.TemperatureGuard)}
	f := []field{}
	for i, label := range labels {
		f = append(f, field{label, strconv.Itoa(int(values[i]))})
	}
	for i, v := range c.TierWatts {
		f = append(f, field{fmt.Sprintf("档位%d上限(W)", i+1), strconv.Itoa(int(v))})
	}
	for i, v := range c.TierRatioPercent {
		f = append(f, field{fmt.Sprintf("档位%d折扣(%%)", i+1), strconv.Itoa(int(v))})
	}
	return f
}
func (m terminalModel) View() string {
	blue := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	status := red.Render("离线 / 注册中")
	if m.state.Online {
		status = green.Render("已连接")
	}
	header := blue.Render("ChargePilot · DC589 设备模拟器") + "  " + status + "\n" + fmt.Sprintf("设备 %s · 软件 %s/%d · 当前端口 %d\n", m.state.Identity.BoardID, m.state.Identity.SoftwareID, m.state.Identity.SoftwareVersion, m.port)
	tabs := []string{"端口与充电", "参数与身份", "待确认上报", "日志"}
	for i, t := range tabs {
		if i == m.tab {
			header += blue.Render("[ " + t + " ] ")
		} else {
			header += t + "  "
		}
	}
	header += "\n" + strings.Repeat("─", max(10, min(m.width-1, 110))) + "\n"
	var body string
	if m.editing {
		body = blue.Render(m.current.label) + "\nEnter 执行 · Tab 切换字段 · Ctrl+U 清空 · Esc 取消\n"
		if len(m.fields) == 0 {
			body += "再次按 Enter 确认执行。"
		} else {
			first := max(0, m.focus-max(1, m.height-13)+1)
			for i := first; i < min(len(m.fields), first+max(1, m.height-12)); i++ {
				f := m.fields[i]
				line := fmt.Sprintf("  %-25s %s", f.label, f.value)
				if i == m.focus {
					line = blue.Render("› " + f.label + "  [" + f.value + "▌]")
				}
				body += line + "\n"
			}
		}
	} else if m.menu {
		body = "操作当前端口 · ↑↓ 选择 · Enter 打开 · Esc 返回\n"
		start := max(0, m.selection-max(1, m.height-12)+1)
		for i := start; i < min(len(terminalActions), start+max(1, m.height-11)); i++ {
			a := terminalActions[i]
			if i == m.selection {
				body += blue.Render("› "+a.label) + "\n"
			} else {
				body += "  " + a.label + "\n"
			}
		}
	} else {
		switch m.tab {
		case 0:
			body = fmt.Sprintf("电压 %d V · 温度 %d ℃ · 烟雾 %t · 待确认 %d\n", m.state.Voltage, m.state.Temperature, m.state.Smoke, len(m.state.Pending))
			body += "端口  插接   状态       功率(W)    充电时长     已用电量     剩余 / 卡号\n"
			active := map[byte]ChargeState{}
			for _, c := range m.state.Charging {
				active[c.Port] = c
			}
			first := max(1, int(m.port)-max(1, m.height-14)+1)
			for i := first; i <= min(len(m.state.Ports), first+max(1, m.height-14)); i++ {
				p := m.state.Ports[byte(i)]
				if p == nil {
					continue
				}
				state := "空闲"
				c, charging := active[byte(i)]
				if charging {
					state = "充电中"
				}
				if p.Fault != 0 {
					state = fmt.Sprintf("故障%d", p.Fault)
				}
				plug := "拔出"
				if p.Connected {
					plug = "插入"
				}
				usage := "—"
				if charging {
					usage = fmt.Sprintf("%s  %.3f度  %s", time.Since(c.Started).Truncate(time.Second), float64(c.Energy)/36/1000000, c.Remaining.Truncate(time.Second))
				}
				line := fmt.Sprintf(" %2d   %s   %-5s   %7.1f    %s  卡:%d", i, plug, state, float64(p.Power)/10, usage, p.Card)
				if byte(i) == m.port {
					line = blue.Render("›" + line)
				}
				body += line + "\n"
			}
		case 1:
			raw, _ := json.MarshalIndent(struct {
				Identity    any
				Config      any
				RemovePower uint16
				Cards       any
				Module      any
			}{m.state.Identity, m.state.Config, m.state.RemovePower, m.state.Cards, m.state.Module}, "", "  ")
			body = string(raw)
		case 2:
			raw, _ := json.MarshalIndent(m.state.Pending, "", "  ")
			body = string(raw)
		case 3:
			body = m.logs.tail(100)
		}
		lines := strings.Split(body, "\n")
		start := min(m.scroll, max(0, len(lines)-1))
		body = strings.Join(lines[start:min(len(lines), start+max(1, m.height-9))], "\n")
	}
	footer := "\n" + red.Render(m.notice) + "\nTab 切页 · ↑↓ 选端口 · P 调功率 · M/Enter 操作 · PgUp/PgDn 滚动 · Q/Ctrl+C 退出（保留状态）"
	out := header + body + footer
	lines := strings.Split(out, "\n")
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], max(10, m.width-1), "…")
	}
	return strings.Join(lines, "\n")
}
