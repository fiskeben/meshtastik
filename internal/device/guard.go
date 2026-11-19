package device

import (
	"meshtastik/internal/transport"
)

// LockoutRisks describes, in human sentences, how applying changes could cut
// off the connection used to apply them, or block later local changes.
func LockoutRisks(kind transport.Kind, before *State, changes []Change) []string {
	var risks []string
	for _, ch := range changes {
		if ch.Config == nil {
			continue
		}
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
			if kind == transport.KindTCP && (was.GetWifiEnabled() || was.GetEthEnabled()) && !now.GetWifiEnabled() && !now.GetEthEnabled() {
				risks = append(risks, "this leaves neither network.wifi_enabled nor network.eth_enabled on, which drops the TCP connection this command is using")
			}
		}
	}
	return risks
}
