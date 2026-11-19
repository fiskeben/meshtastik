package cli

import (
	"fmt"
	"sort"
	"text/tabwriter"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"github.com/spf13/cobra"
)

func nodeID(num uint32) string { return fmt.Sprintf("!%08x", num) }

// batteryOf returns the node's battery level, or nil without device metrics.
func batteryOf(n *pb.NodeInfo) *uint32 {
	if n == nil || n.GetDeviceMetrics() == nil || !n.GetDeviceMetrics().HasBatteryLevel() {
		return nil
	}
	v := n.GetDeviceMetrics().GetBatteryLevel()
	return &v
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func batteryText(p *uint32) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%d%%", *p)
}

func newInfoCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show the connected node's identity and basics",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			st := c.State()
			self := st.Self()
			num := st.MyInfo.GetMyNodeNum()
			channels := 0
			for _, ch := range st.Channels {
				if ch != nil && ch.GetRole() != pb.Channel_DISABLED {
					channels++
				}
			}
			bat := batteryOf(self)
			if a.asJSON {
				return printJSON(a.Stdout, map[string]any{
					"id":            nodeID(num),
					"num":           num,
					"long_name":     self.GetUser().GetLongName(),
					"short_name":    self.GetUser().GetShortName(),
					"hw_model":      self.GetUser().GetHwModel().String(),
					"firmware":      st.Metadata.GetFirmwareVersion(),
					"region":        st.Config.GetLora().GetRegion().String(),
					"role":          st.Config.GetDevice().GetRole().String(),
					"modem_preset":  st.Config.GetLora().GetModemPreset().String(),
					"battery_level": bat,
					"channels":      channels,
				})
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			row := func(k, v string) { fmt.Fprintf(tw, "%s:\t%s\n", k, v) }
			row("ID", nodeID(num))
			row("Name", orDash(self.GetUser().GetLongName()))
			row("Short name", orDash(self.GetUser().GetShortName()))
			row("Hardware", self.GetUser().GetHwModel().String())
			row("Firmware", orDash(st.Metadata.GetFirmwareVersion()))
			row("Region", st.Config.GetLora().GetRegion().String())
			row("Role", st.Config.GetDevice().GetRole().String())
			row("Modem preset", st.Config.GetLora().GetModemPreset().String())
			row("Battery", batteryText(bat))
			row("Channels", fmt.Sprint(channels))
			return tw.Flush()
		},
	}
}

type nodeRow struct {
	ID           string  `json:"id"`
	Num          uint32  `json:"num"`
	LongName     string  `json:"long_name"`
	ShortName    string  `json:"short_name"`
	LastHeard    *string `json:"last_heard"`
	SNR          float32 `json:"snr"`
	HopsAway     *uint32 `json:"hops_away"`
	BatteryLevel *uint32 `json:"battery_level"`
}

// relTime formats a unix timestamp as e.g. "5m ago"; "-" when unknown.
func relTime(unix uint32, now time.Time) string {
	if unix == 0 {
		return "-"
	}
	d := now.Sub(time.Unix(int64(unix), 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func newNodesCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "nodes",
		Short: "List nodes known to the connected node",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			nodes := make([]*pb.NodeInfo, 0)
			for _, n := range c.State().Nodes {
				nodes = append(nodes, n)
			}
			sort.Slice(nodes, func(i, j int) bool {
				if nodes[i].GetLastHeard() != nodes[j].GetLastHeard() {
					return nodes[i].GetLastHeard() > nodes[j].GetLastHeard()
				}
				return nodes[i].GetNum() < nodes[j].GetNum()
			})
			rows := make([]nodeRow, 0, len(nodes))
			for _, n := range nodes {
				r := nodeRow{
					ID: nodeID(n.GetNum()), Num: n.GetNum(),
					LongName: n.GetUser().GetLongName(), ShortName: n.GetUser().GetShortName(),
					SNR: n.GetSnr(), BatteryLevel: batteryOf(n),
				}
				if n.GetLastHeard() != 0 {
					s := time.Unix(int64(n.GetLastHeard()), 0).UTC().Format(time.RFC3339)
					r.LastHeard = &s
				}
				if n.HasHopsAway() {
					h := n.GetHopsAway()
					r.HopsAway = &h
				}
				rows = append(rows, r)
			}
			if a.asJSON {
				return printJSON(a.Stdout, rows)
			}
			now := time.Now()
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tSHORT\tLAST HEARD\tSNR\tHOPS\tBATTERY")
			for i, r := range rows {
				hops := "-"
				if r.HopsAway != nil {
					hops = fmt.Sprint(*r.HopsAway)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%.1f\t%s\t%s\n", r.ID, orDash(r.LongName), orDash(r.ShortName),
					relTime(nodes[i].GetLastHeard(), now), r.SNR, hops, batteryText(r.BatteryLevel))
			}
			return tw.Flush()
		},
	}
}
