package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"sigs.k8s.io/yaml"

	"meshtastik/internal/backup"
	"meshtastik/internal/channelurl"
	"meshtastik/internal/configpath"
	"meshtastik/internal/device"
)

// confirmOrAbort returns nil when --yes was given or the user confirms prompt.
func confirmOrAbort(a *app, prompt string) error {
	if a.yes || (a.Confirm != nil && a.Confirm(prompt)) {
		return nil
	}
	return errors.New("not confirmed; nothing was changed (pass --yes to skip confirmation)")
}

func newConfigCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Read and change the node's configuration"}
	cmd.AddCommand(newConfigGetCmd(a), newConfigSetCmd(a),
		newConfigExportCmd(a), newConfigImportCmd(a))
	return cmd
}

func newConfigGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "get [path]",
		Short: "Show the whole configuration, a section, or one field (e.g. lora.region)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			st := c.State()
			if len(args) == 0 {
				return printAllConfig(a, st)
			}
			p, err := configpath.Parse(args[0])
			if err != nil {
				return err
			}
			sec := configpath.Section(st.Config, st.ModuleConfig, p)
			if p.Field == "" {
				return printProto(a.Stdout, sec.Interface(), a.asJSON)
			}
			v, err := configpath.FormatField(sec, p.Field)
			if err != nil {
				return err
			}
			if a.asJSON {
				return printJSON(a.Stdout, map[string]string{"path": p.String(), "value": v})
			}
			_, err = fmt.Fprintln(a.Stdout, v)
			return err
		},
	}
}

func printAllConfig(a *app, st *device.State) error {
	mo := protojson.MarshalOptions{UseProtoNames: true}
	cfg, err := mo.Marshal(st.Config)
	if err != nil {
		return err
	}
	mod, err := mo.Marshal(st.ModuleConfig)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]json.RawMessage{"config": cfg, "module_config": mod})
	if err != nil {
		return err
	}
	if a.asJSON {
		var buf bytes.Buffer
		if err := json.Indent(&buf, raw, "", "  "); err != nil {
			return err
		}
		_, err = fmt.Fprintln(a.Stdout, buf.String())
		return err
	}
	y, err := yaml.JSONToYAML(raw)
	if err != nil {
		return err
	}
	_, err = a.Stdout.Write(y)
	return err
}

func newConfigSetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "set <path> <value> [<path> <value>...]",
		Short: "Set one or more config fields in a single verified edit",
		Example: "  meshtastik config set lora.hop_limit 5 lora.tx_power 20\n" +
			"  meshtastik config set -- lora.tx_power -5   # negative values need --",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 || len(args)%2 != 0 {
				return errors.New("expected <path> <value> pairs")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate every pair before touching the node.
			paths := make([]configpath.Path, 0, len(args)/2)
			for i := 0; i < len(args); i += 2 {
				p, err := configpath.Parse(args[i])
				if err != nil {
					return err
				}
				paths = append(paths, p)
			}
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			st := c.State()
			cfg := proto.Clone(st.Config).(*pb.LocalConfig)
			mod := proto.Clone(st.ModuleConfig).(*pb.LocalModuleConfig)
			for i, p := range paths {
				if err := configpath.Set(cfg, mod, p, args[2*i+1]); err != nil {
					return fmt.Errorf("%s: %w", args[2*i], err)
				}
			}
			cs, ms := configpath.Changes(st.Config, cfg, st.ModuleConfig, mod)
			var changes []device.Change
			for _, s := range cs {
				changes = append(changes, device.Change{Config: s})
			}
			for _, s := range ms {
				changes = append(changes, device.Change{ModuleConfig: s})
			}
			return applyChanges(cmd, a, c, changes)
		},
	}
}

// applyChanges checks changes for lock-out risks, confirms if there are any,
// applies them with read-back verification and reports the result.
func applyChanges(cmd *cobra.Command, a *app, c *device.Client, changes []device.Change) error {
	if risks := device.LockoutRisks(c.Kind(), c.State(), changes); len(risks) > 0 {
		for _, r := range risks {
			fmt.Fprintf(a.Stderr, "Warning: %s\n", r)
		}
		if err := confirmOrAbort(a, "Apply anyway?"); err != nil {
			return err
		}
	}
	n, err := c.Apply(cmd.Context(), changes)
	if err != nil {
		return err
	}
	if n == 0 {
		_, err = fmt.Fprintln(a.Stdout, "No changes needed.")
		return err
	}
	_, err = fmt.Fprintf(a.Stdout, "Applied %d change(s); verified.\n", n)
	return err
}

func newConfigExportCmd(a *app) *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "export [-o file]",
		Short: "Write the node's configuration as a Python-compatible YAML backup",
		Long: "Write the node's configuration as YAML compatible with the Python CLI's --export-config.\n" +
			"The backup contains the node's private key; a file is written with mode 0600.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			data, err := backup.Export(c.State())
			if err != nil {
				return err
			}
			if out == "" {
				_, err = a.Stdout.Write(data)
				return err
			}
			if err := os.WriteFile(out, data, 0o600); err != nil {
				return err
			}
			// WriteFile keeps the mode of an existing file.
			return os.Chmod(out, 0o600)
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "write to this file instead of stdout")
	return cmd
}

func newConfigImportCmd(a *app) *cobra.Command {
	var keys bool
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Apply a YAML backup in a single verified edit",
		Long: "Apply a backup written by config export or the Python CLI's --export-config.\n" +
			"Only keys present in the file are changed. The node's private and public keys\n" +
			"are left alone unless --keys is given.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			doc, err := backup.Read(data)
			if err != nil {
				return err
			}
			var set *pb.ChannelSet
			if doc.ChannelURL != "" {
				if set, err = channelurl.Decode(doc.ChannelURL); err != nil {
					return err
				}
				if len(set.Settings) > 8 {
					return fmt.Errorf("channel_url has %d channels; a node holds at most 8", len(set.Settings))
				}
				for _, s := range set.Settings {
					if err := checkChannelName(s.GetName()); err != nil {
						return err
					}
				}
			}
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			st := c.State()
			base := proto.Clone(st.Config).(*pb.LocalConfig)
			if set != nil && set.LoraConfig != nil {
				base.Lora = set.LoraConfig
			}
			cfg, skipped, err := doc.MergeConfig(base, keys)
			if err != nil {
				return err
			}
			mod, err := doc.MergeModuleConfig(st.ModuleConfig)
			if err != nil {
				return err
			}
			var changes []device.Change
			if set != nil {
				changes = channelSetChanges(st.Channels, set)
			}
			cs, ms := configpath.Changes(st.Config, cfg, st.ModuleConfig, mod)
			for _, s := range cs {
				changes = append(changes, device.Change{Config: s})
			}
			for _, s := range ms {
				changes = append(changes, device.Change{ModuleConfig: s})
			}
			self := st.Self()
			if doc.Owner != "" || doc.OwnerShort != "" {
				owner := &pb.User{LongName: self.GetUser().GetLongName(), ShortName: self.GetUser().GetShortName()}
				if doc.Owner != "" {
					owner.LongName = doc.Owner
				}
				if doc.OwnerShort != "" {
					owner.ShortName = doc.OwnerShort
				}
				if owner.LongName != self.GetUser().GetLongName() || owner.ShortName != self.GetUser().GetShortName() {
					changes = append(changes, device.Change{Owner: owner})
				}
			}
			if l := doc.Location; l != nil {
				pos := &pb.Position{
					LatitudeI:  proto.Int32(int32(math.Round(l.Lat * 1e7))),
					LongitudeI: proto.Int32(int32(math.Round(l.Lon * 1e7))),
					Altitude:   proto.Int32(l.Alt),
				}
				cur := self.GetPosition()
				if !st.Config.GetPosition().GetFixedPosition() || cur.GetLatitudeI() != pos.GetLatitudeI() ||
					cur.GetLongitudeI() != pos.GetLongitudeI() || cur.GetAltitude() != pos.GetAltitude() {
					changes = append(changes, device.Change{FixedPosition: pos})
				}
			}
			for _, k := range skipped {
				fmt.Fprintf(a.Stderr, "skipped: %s (pass --keys to apply)\n", k)
			}
			for _, k := range doc.Unsupported {
				fmt.Fprintf(a.Stderr, "not supported, ignored: %s\n", k)
			}
			return applyChanges(cmd, a, c, changes)
		},
	}
	cmd.Flags().BoolVar(&keys, "keys", false, "also apply security.private_key and security.public_key")
	return cmd
}
