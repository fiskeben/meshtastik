package device_test

import (
	"strings"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/device"
	"meshtastik/internal/transport"
)

func TestLockoutRisks(t *testing.T) {
	st := &device.State{
		Config: &pb.LocalConfig{
			Bluetooth: &pb.Config_BluetoothConfig{Enabled: true}, Security: &pb.Config_SecurityConfig{SerialEnabled: true},
			Network: &pb.Config_NetworkConfig{WifiEnabled: true, WifiSsid: "home", WifiPsk: "secret"}},
		ModuleConfig: &pb.LocalModuleConfig{Serial: &pb.ModuleConfig_SerialConfig{}},
	}
	cfg := func(c *pb.Config) []device.Change { return []device.Change{{Config: c}} }
	net := func(n *pb.Config_NetworkConfig) []device.Change {
		return cfg(&pb.Config{PayloadVariant: &pb.Config_Network{Network: n}})
	}
	btOff := cfg(&pb.Config{PayloadVariant: &pb.Config_Bluetooth{Bluetooth: &pb.Config_BluetoothConfig{}}})
	serialOff := cfg(&pb.Config{PayloadVariant: &pb.Config_Security{Security: &pb.Config_SecurityConfig{}}})
	wifiOff := net(&pb.Config_NetworkConfig{})
	wifiOffEthOn := net(&pb.Config_NetworkConfig{EthEnabled: true, WifiSsid: "home", WifiPsk: "secret"})
	managed := cfg(&pb.Config{PayloadVariant: &pb.Config_Security{Security: &pb.Config_SecurityConfig{SerialEnabled: true, IsManaged: true}}})
	sameNet := net(&pb.Config_NetworkConfig{WifiEnabled: true, WifiSsid: "home", WifiPsk: "secret"})
	newSSID := net(&pb.Config_NetworkConfig{WifiEnabled: true, WifiSsid: "cabin", WifiPsk: "secret"})
	newPSK := net(&pb.Config_NetworkConfig{WifiEnabled: true, WifiSsid: "home", WifiPsk: "other"})
	static := net(&pb.Config_NetworkConfig{WifiEnabled: true, WifiSsid: "home", WifiPsk: "secret", AddressMode: pb.Config_NetworkConfig_STATIC})
	newIP := net(&pb.Config_NetworkConfig{WifiEnabled: true, WifiSsid: "home", WifiPsk: "secret", Ipv4Config: &pb.Config_NetworkConfig_IpV4Config{Ip: 0x0a000002}})
	overrideConsole := []device.Change{{ModuleConfig: &pb.ModuleConfig{PayloadVariant: &pb.ModuleConfig_Serial{
		Serial: &pb.ModuleConfig_SerialConfig{OverrideConsoleSerialPort: true}}}}}
	cases := []struct {
		name  string
		k     transport.Kind
		ch    []device.Change
		risky bool
	}{
		{"bt off over ble", transport.KindBLE, btOff, true}, {"bt off over serial", transport.KindSerial, btOff, false},
		{"serial off", transport.KindSerial, serialOff, true}, {"wifi off over tcp", transport.KindTCP, wifiOff, true},
		{"wifi off eth on", transport.KindTCP, wifiOffEthOn, false}, {"managed", transport.KindSerial, managed, true},
		{"same network over tcp", transport.KindTCP, sameNet, false},
		{"ssid over tcp", transport.KindTCP, newSSID, true}, {"ssid over serial", transport.KindSerial, newSSID, false},
		{"psk over tcp", transport.KindTCP, newPSK, true}, {"static over tcp", transport.KindTCP, static, true},
		{"ipv4 over tcp", transport.KindTCP, newIP, true},
		{"override console over serial", transport.KindSerial, overrideConsole, true},
		{"override console over tcp", transport.KindTCP, overrideConsole, false},
	}
	for _, c := range cases {
		if got := len(device.LockoutRisks(c.k, st, c.ch)) > 0; got != c.risky {
			t.Errorf("%s: risky=%v", c.name, got)
		}
	}
}

func TestLockoutRiskWifiOverBLE(t *testing.T) {
	st := &device.State{Config: &pb.LocalConfig{Network: &pb.Config_NetworkConfig{}}, ModuleConfig: &pb.LocalModuleConfig{}}
	wifiOn := []device.Change{{Config: &pb.Config{PayloadVariant: &pb.Config_Network{Network: &pb.Config_NetworkConfig{WifiEnabled: true}}}}}
	risks := device.LockoutRisks(transport.KindBLE, st, wifiOn)
	if len(risks) != 1 || !strings.Contains(risks[0], "on ESP32 boards, enabling WiFi disables Bluetooth") {
		t.Fatal(risks)
	}
	if r := device.LockoutRisks(transport.KindSerial, st, wifiOn); len(r) != 0 {
		t.Fatal(r)
	}
	st.Config.Network.WifiEnabled = true
	if r := device.LockoutRisks(transport.KindBLE, st, wifiOn); len(r) != 0 {
		t.Fatal("wifi already on:", r)
	}
}
