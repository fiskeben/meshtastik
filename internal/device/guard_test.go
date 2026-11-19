package device_test

import (
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/device"
	"meshtastik/internal/transport"
)

func TestLockoutRisks(t *testing.T) {
	st := &device.State{Config: &pb.LocalConfig{
		Bluetooth: &pb.Config_BluetoothConfig{Enabled: true}, Security: &pb.Config_SecurityConfig{SerialEnabled: true},
		Network: &pb.Config_NetworkConfig{WifiEnabled: true}}}
	cfg := func(c *pb.Config) []device.Change { return []device.Change{{Config: c}} }
	btOff := cfg(&pb.Config{PayloadVariant: &pb.Config_Bluetooth{Bluetooth: &pb.Config_BluetoothConfig{}}})
	serialOff := cfg(&pb.Config{PayloadVariant: &pb.Config_Security{Security: &pb.Config_SecurityConfig{}}})
	wifiOff := cfg(&pb.Config{PayloadVariant: &pb.Config_Network{Network: &pb.Config_NetworkConfig{}}})
	wifiOffEthOn := cfg(&pb.Config{PayloadVariant: &pb.Config_Network{Network: &pb.Config_NetworkConfig{EthEnabled: true}}})
	managed := cfg(&pb.Config{PayloadVariant: &pb.Config_Security{Security: &pb.Config_SecurityConfig{SerialEnabled: true, IsManaged: true}}})
	cases := []struct {
		k     transport.Kind
		ch    []device.Change
		risky bool
	}{
		{transport.KindBLE, btOff, true}, {transport.KindSerial, btOff, false},
		{transport.KindSerial, serialOff, true}, {transport.KindTCP, wifiOff, true},
		{transport.KindTCP, wifiOffEthOn, false}, {transport.KindSerial, managed, true},
	}
	for i, c := range cases {
		if got := len(device.LockoutRisks(c.k, st, c.ch)) > 0; got != c.risky {
			t.Errorf("case %d: risky=%v", i, got)
		}
	}
}
