// Command simulator-dc589 在本地顶替一块 dc589 充电板。
//
// 它是开发与测试工具，对运行中的网关讲真实的 5.8.9 线路协议，
// 因此无需厂商硬件也能走通充电链路。
// 绝不能在生产进程中启动。
//
// 一种协议一个命令：
// 换厂商或换成 MQTT 板时，在本命令旁边另起一个。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/protocol/dc589"
	"github.com/ChargePilot2026/charge-pilot/internal/gateway/simulator/dc589"
)

// 板号在线路上是十六位十进制数字，
// 所以未指定时用一个一眼就是编造的默认值。
const defaultBoardID = "5348240514082652"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "simulator-dc589:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		gateway       = flag.String("gateway", "127.0.0.1:9100", "gateway TCP address (host:port)")
		boardID       = flag.String("board-id", defaultBoardID, "board id, exactly sixteen decimal digits")
		hardware      = flag.String("hardware", "SH10HP01", "hardware version, eight characters")
		softwareID    = flag.String("software-id", "DC589SW1", "software id, eight characters")
		software      = flag.Int("software-version", 107, "software version number")
		module        = flag.String("module", "1234567812345678", "module id, sixteen digits")
		sim           = flag.String("sim", "89860000000000000001", "SIM id, twenty digits")
		strength      = flag.Int("signal", 4, "reported signal strength")
		ports         = flag.Int("ports", 2, "number of charging ports on the board")
		scenario      = flag.String("scenario", string(dc589sim.ScenarioByEnergy), "behaviour: by-energy, by-time, stop-on-command, reject-start, fault, silent, reconnect")
		heartbeat     = flag.Duration("heartbeat", 15*time.Second, "heartbeat interval")
		power         = flag.Uint("power", 1500, "draw while charging, in tenths of a watt")
		reconnect     = flag.Duration("reconnect-after", 3*time.Second, "pause before reconnecting in the reconnect scenario")
		faultAfter    = flag.Duration("fault-after", 5*time.Second, "delay before reporting a fault in the fault scenario")
		autoMake      = flag.Bool("auto-provision", false, "provision the board before registering, if it does not exist yet")
		provisionBase = flag.String("provision-url", "http://127.0.0.1:8083", "gateway internal HTTP base URL used for provisioning")
		vendorID      = flag.Uint64("vendor-id", 0, "vendor id for provisioning")
		stationID     = flag.Uint64("station-id", 0, "station id for provisioning")
		token         = flag.String("service-token", os.Getenv("SERVICE_TOKEN"), "service token for the provisioning call")
		control       = flag.String("control", "127.0.0.1:9190", "loopback HTTP listener for /state and /events; empty disables it")
		stateFile     = flag.String("state-file", "", "persist configuration, ports, active charges and pending reports in this JSON file")
		tui           = flag.Bool("tui", true, "interactive terminal UI; set false for headless/CI")
	)
	flag.Parse()
	if *ports < 1 || *ports > 20 || *power > 65535 || *software < 0 || *software > 65535 || *strength < 0 || *strength > 255 {
		return errors.New("ports must be 1-20; power/software 0-65535; signal 0-255")
	}

	logger := log.New(os.Stdout, "dc589sim ", log.LstdFlags)
	if err := checkBoardID(*boardID); err != nil {
		return err
	}
	choice := dc589sim.Scenario(*scenario)
	if err := checkScenario(choice); err != nil {
		return err
	}

	config := dc589sim.Config{
		Identity: dc589.DeviceIdentity{
			BoardID:         *boardID,
			HardwareVersion: *hardware,
			SoftwareID:      *softwareID,
			SoftwareVersion: uint16(*software),
			ModuleID:        *module,
			SIM:             *sim,
			Signal:          byte(*strength),
		},
		PortCount:      *ports,
		Gateway:        *gateway,
		Scenario:       choice,
		Heartbeat:      *heartbeat,
		PowerDeciWatts: uint32(*power),
		ReconnectAfter: *reconnect,
		FaultAfter:     *faultAfter,
		Log:            logger,
	}

	if *autoMake {
		if err := provision(provisionEndpoint(*provisionBase), *token, *boardID, *vendorID, *stationID, uint8(*ports)); err != nil {
			return fmt.Errorf("provision: %w", err)
		}
		logger.Printf("device %s is ready to register", *boardID)
	} else {
		logger.Printf("registering as %s without provisioning; the gateway rejects unknown devices", *boardID)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config.StateFile = *stateFile
	inputs := make(chan dc589sim.Input, 16)
	config.Inputs = inputs
	if *control != "" {
		host, _, err := net.SplitHostPort(*control)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("control listener must use a loopback IP")
		}
		listener, err := net.Listen("tcp", *control)
		if err != nil {
			return err
		}
		server := &http.Server{Handler: dc589sim.ControlHandler(ctx, inputs), ReadHeaderTimeout: 5 * time.Second}
		defer server.Close()
		go func() {
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Printf("control: %v", err)
				stop()
			}
		}()
		logger.Printf("physical controls: http://%s/state and POST /events", *control)
	}
	logger.Printf("connecting to %s, scenario %s", *gateway, choice)
	if *tui {
		return dc589sim.RunTUI(ctx, config, inputs, stop)
	}
	return dc589sim.Run(ctx, config)
}

// checkBoardID 在建立任何连接之前就拒绝帧承载不了的标识符，
// 这样报错会指出真正原因，而不是以一个
// 无从解读的注册被拒呈现。
func checkBoardID(id string) error {
	if len(id) != dc589.BoardIDDigits {
		return fmt.Errorf("board id %q has %d characters; a 5.8.9 board id is exactly %d decimal digits", id, len(id), dc589.BoardIDDigits)
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return fmt.Errorf("board id %q contains %q; a 5.8.9 board id is decimal digits only", id, r)
		}
	}
	return nil
}

func checkScenario(s dc589sim.Scenario) error {
	known := map[dc589sim.Scenario]bool{
		dc589sim.ScenarioByEnergy:      true,
		dc589sim.ScenarioByTime:        true,
		dc589sim.ScenarioStopOnCommand: true,
		dc589sim.ScenarioRejectStart:   true,
		dc589sim.ScenarioFault:         true,
		dc589sim.ScenarioSilent:        true,
		dc589sim.ScenarioReconnect:     true,
	}
	if !known[s] {
		names := make([]string, 0, len(known))
		for name := range known {
			names = append(names, string(name))
		}
		return fmt.Errorf("unknown scenario %q; choose one of %s", s, strings.Join(names, ", "))
	}
	return nil
}

func provisionEndpoint(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/api/v1/internal/devices/provision"
}

// provision 通过网关内部 API 注册这块板。
// 已存在的设备原样放过，
// 因为该接口设计上就是幂等的，而重复开通不能打扰正在充电的设备。
func provision(endpoint, token, deviceID string, vendorID, stationID uint64, ports uint8) error {
	if token == "" {
		return errors.New("provisioning needs a service token; pass -service-token or set SERVICE_TOKEN")
	}
	if vendorID == 0 || stationID == 0 {
		return errors.New("provisioning needs -vendor-id and -station-id")
	}
	body, err := json.Marshal(map[string]any{"devices": []map[string]any{{
		"device_id": deviceID, "vendor_id": vendorID, "station_id": stationID, "port_count": ports,
	}}})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Service-Token", token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return fmt.Errorf("provision returned %s", response.Status)
	}
	return nil
}
