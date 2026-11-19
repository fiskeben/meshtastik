package device

import (
	"errors"
	"strings"

	"google.golang.org/protobuf/proto"

	"meshtastik/internal/transport"
)

// LockoutRisks describes, in human sentences, how applying changes could cut
// off the connection used to apply them, or block later local changes.
func LockoutRisks(kind transport.Kind, before *State, changes []Change) []string {
	var risks []string
	for _, ch := range changes {
		switch {
		case ch.Config.GetBluetooth() != nil:
			if kind == transport.KindBLE && before.Config.GetBluetooth().GetEnabled() && !ch.Config.GetBluetooth().GetEnabled() {
				risks = append(risks, "bluetooth.enabled=false disables Bluetooth, the link this command is using")
			}
		case ch.Config.GetSecurity() != nil:
			was, now := before.Config.GetSecurity(), ch.Config.GetSecurity()
			if kind == transport.KindSerial && was.GetSerialEnabled() && !now.GetSerialEnabled() {
				risks = append(risks, "security.serial_enabled=false disables the serial console, the link this command is using")
			}
			if !was.GetIsManaged() && now.GetIsManaged() {
				risks = append(risks, "security.is_managed=true means local config will be blocked; the node can then only be configured by a remote admin")
			}
		case ch.Config.GetNetwork() != nil:
			was, now := before.Config.GetNetwork(), ch.Config.GetNetwork()
			if kind == transport.KindBLE && !was.GetWifiEnabled() && now.GetWifiEnabled() {
				risks = append(risks, "network.wifi_enabled=true: on ESP32 boards, enabling WiFi disables Bluetooth, the link this command is using")
			}
			if kind != transport.KindTCP {
				break
			}
			if (was.GetWifiEnabled() || was.GetEthEnabled()) && !now.GetWifiEnabled() && !now.GetEthEnabled() {
				risks = append(risks, "this leaves neither network.wifi_enabled nor network.eth_enabled on, which drops the TCP connection this command is using")
				break
			}
			var changed []string
			if was.GetWifiSsid() != now.GetWifiSsid() {
				changed = append(changed, "network.wifi_ssid")
			}
			if was.GetWifiPsk() != now.GetWifiPsk() {
				changed = append(changed, "network.wifi_psk")
			}
			if was.GetAddressMode() != now.GetAddressMode() {
				changed = append(changed, "network.address_mode")
			}
			if !proto.Equal(was.GetIpv4Config(), now.GetIpv4Config()) {
				changed = append(changed, "network.ipv4_config")
			}
			if len(changed) > 0 {
				risks = append(risks, "changing "+strings.Join(changed, ", ")+" drops the TCP connection this command is using, and the node's address may change")
			}
		case ch.ModuleConfig.GetSerial() != nil:
			was, now := before.ModuleConfig.GetSerial(), ch.ModuleConfig.GetSerial()
			if kind == transport.KindSerial && !was.GetOverrideConsoleSerialPort() && now.GetOverrideConsoleSerialPort() {
				risks = append(risks, "serial.override_console_serial_port=true hands the USB serial console to the serial module, cutting off the link this command is using")
			}
		}
	}
	return risks
}

// errManaged is returned by ManagedRefusal.
var errManaged = errors.New("node is managed (security.is_managed); local configuration changes are ignored by the firmware")

// ManagedRefusal returns an error when the node is managed and changes holds
// anything but the security section turning is_managed off. Changes equal to
// the current state are ignored.
func ManagedRefusal(before *State, changes []Change) error {
	if !before.Config.GetSecurity().GetIsManaged() {
		return nil
	}
	for _, ch := range changes {
		if sec := ch.Config.GetSecurity(); sec != nil && !sec.GetIsManaged() {
			continue
		}
		if ch.validate() == nil && len(ch.diff(before)) == 0 {
			continue
		}
		return errManaged
	}
	return nil
}
