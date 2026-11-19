package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"meshtastik/internal/transport"
)

type bleRow struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	RSSI    int16  `json:"rssi"`
}

func newBLECmd(a *app) *cobra.Command {
	ble := &cobra.Command{Use: "ble", Short: "Bluetooth LE helpers"}
	var duration time.Duration
	scan := &cobra.Command{
		Use:   "scan",
		Short: "Scan for nearby Meshtastic devices over Bluetooth LE",
		Long: "Scan for nearby Meshtastic devices over Bluetooth LE.\n\n" +
			"Use the NAME or ADDRESS with --ble. On macOS the address is a CoreBluetooth UUID, not a MAC address.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			devs, err := transport.ScanBLE(cmd.Context(), duration)
			if err != nil {
				return err
			}
			rows := make([]bleRow, 0, len(devs))
			for _, d := range devs {
				rows = append(rows, bleRow{Name: d.Name, Address: d.Address, RSSI: d.RSSI})
			}
			if a.asJSON {
				return printJSON(a.Stdout, rows)
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tADDRESS\tRSSI")
			for _, r := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%d\n", orDash(r.Name), r.Address, r.RSSI)
			}
			return tw.Flush()
		},
	}
	scan.Flags().DurationVar(&duration, "duration", 10*time.Second, "how long to scan")
	ble.AddCommand(scan)
	return ble
}
