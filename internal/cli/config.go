package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/backup"
	"meshtastik/internal/channelurl"
	"meshtastik/internal/configpath"
	"meshtastik/internal/device"
)

// confirmOrAbort returns nil when --yes was given or the user confirms prompt.
// An interrupt (ctx done) while waiting for the answer aborts.
func confirmOrAbort(ctx context.Context, a *app, prompt string) error {
	if a.yes {
		return nil
	}
	if a.acting != "" {
		prompt = a.acting + ": " + prompt
	}
	if a.Confirm != nil {
		answer := make(chan bool, 1)
		go func() { answer <- a.Confirm(prompt) }()
		select {
		case ok := <-answer:
			if ok {
				return nil
			}
		case <-ctx.Done():
			fmt.Fprintln(a.Stderr) // end the prompt line
			return errors.New("interrupted; nothing was changed")
		}
	}
	return errors.New("not confirmed; nothing was changed (pass --yes to skip confirmation)")
}

func newConfigCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Read and change the node's configuration"}
	cmd.AddCommand(supportsDest(newConfigGetCmd(a)), supportsDest(changes(newConfigSetCmd(a), "yes")),
		supportsDest(newConfigExportCmd(a)), supportsDest(changes(newConfigImportCmd(a), "yes")))
	return cmd
}

func newConfigGetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "get [path]",
		Short: "Show the whole configuration, a section, or one field (e.g. lora.region)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			want := allWant
			var p configpath.Path
			if len(args) == 1 {
				var err error
				if p, err = configpath.Parse(args[0]); err != nil {
					return err
				}
				want = sectionWant(p.Section)
			}
			c, _, st, err := a.target(cmd, want)
			if err != nil {
				return err
			}
			defer c.Close()
			if len(args) == 0 {
				return printAllConfig(a, st)
			}
			if p.Field == "" {
				sec := configpath.Section(redactKey(st.Config), st.ModuleConfig, p)
				raw, err := protoOutput.Marshal(sec.Interface())
				if err != nil {
					return err
				}
				return printRaw(a.Stdout, unredact(raw), a.asJSON)
			}
			sec := configpath.Section(st.Config, st.ModuleConfig, p)
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

// redactedKey is shown instead of a non-empty security.private_key: config
// get never prints the key unless that exact field is asked for.
const redactedKey = "<redacted; use config export>"

// keySentinel stands in for the private key while marshalling; unredact then
// swaps its base64 form for redactedKey.
var keySentinel = []byte("meshtastik:redacted-private-key")

// redactKey returns cfg, or a copy with a non-empty private key replaced by
// keySentinel.
func redactKey(cfg *pb.LocalConfig) *pb.LocalConfig {
	if len(cfg.GetSecurity().GetPrivateKey()) == 0 {
		return cfg
	}
	out := proto.Clone(cfg).(*pb.LocalConfig)
	out.Security.PrivateKey = keySentinel
	return out
}

// unredact replaces the JSON string holding keySentinel with redactedKey.
func unredact(raw []byte) []byte {
	quoted := func(s string) []byte { return []byte(`"` + s + `"`) }
	return bytes.ReplaceAll(raw, quoted(base64.StdEncoding.EncodeToString(keySentinel)), quoted(redactedKey))
}

func printAllConfig(a *app, st *device.State) error {
	cfg, err := protoOutput.Marshal(redactKey(st.Config))
	if err != nil {
		return err
	}
	mod, err := protoOutput.Marshal(st.ModuleConfig)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]json.RawMessage{"config": unredact(cfg), "module_config": mod}); err != nil {
		return err
	}
	return printRaw(a.Stdout, buf.Bytes(), a.asJSON)
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
				if a.dest != "" && p.Section == "security" && (p.Field == "private_key" || p.Field == "public_key") {
					return fmt.Errorf("%s cannot be set with --dest: a node's keys must not be sent over the air", p)
				}
				paths = append(paths, p)
			}
			var want device.Want
			for _, p := range paths {
				w := sectionWant(p.Section)
				want.Config, want.Modules = append(want.Config, w.Config...), append(want.Modules, w.Modules...)
			}
			c, n, st, err := a.target(cmd, want)
			if err != nil {
				return err
			}
			defer c.Close()
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
			return applyChanges(cmd, a, c, n, st, changes)
		},
	}
}

// applyChanges checks changes to node n (whose state is before) for lock-out
// risks, confirms if there are any, applies them with read-back
// verification and reports the result.
func applyChanges(cmd *cobra.Command, a *app, c *device.Client, n device.Node, before *device.State, changes []device.Change) error {
	var risks []string
	if n.IsRemote() {
		risks = device.RemoteRisks(before, c.State().Config.GetSecurity().GetPublicKey(), changes)
	} else {
		if err := device.ManagedRefusal(before, changes); err != nil {
			return err
		}
		risks = device.LockoutRisks(c.Kind(), before, changes)
	}
	if len(risks) > 0 {
		for _, r := range risks {
			fmt.Fprintf(a.Stderr, "Warning: %s\n", r)
		}
		if err := confirmOrAbort(cmd.Context(), a, "Apply anyway?"); err != nil {
			return err
		}
	}
	applied, err := n.Apply(cmd.Context(), changes)
	note := ""
	if errors.Is(err, device.ErrCoordinatesUnverified) {
		note, err = err.Error(), nil
	}
	if err != nil {
		return err
	}
	if applied == 0 {
		_, err = fmt.Fprintln(a.Stdout, "No changes needed.")
		return err
	}
	fmt.Fprintf(a.Stdout, "Applied %d change(s); verified.\n", applied)
	if note != "" {
		fmt.Fprintf(a.Stderr, "note: %s\n", note)
	}
	return nil
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
			c, n, st, err := a.target(cmd, allWant)
			if err != nil {
				return err
			}
			defer c.Close()
			data, err := backup.Export(st)
			if err != nil {
				return err
			}
			if n.IsRemote() {
				fmt.Fprintln(a.Stderr, "note: a remote node never sends its private key; the backup has none")
				if st.Config.GetPosition().GetFixedPosition() {
					fmt.Fprintln(a.Stderr, "note: location omitted: a remote node's fixed position cannot be read")
				}
			}
			if out == "" {
				_, err = a.Stdout.Write(data)
				return err
			}
			return writeFileAtomic(out, data)
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
			if keys && a.dest != "" {
				return errors.New("--keys cannot be used with --dest: a private key must not be sent over the air")
			}
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			doc, err := backup.Read(data)
			if err != nil {
				return err
			}
			if doc.Owner != "" {
				if err := checkLongName(doc.Owner); err != nil {
					return fmt.Errorf("owner: %w", err)
				}
			}
			if doc.OwnerShort != "" {
				if err := checkShortName(doc.OwnerShort); err != nil {
					return fmt.Errorf("owner_short: %w", err)
				}
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
			c, n, st, err := a.target(cmd, allWant)
			if err != nil {
				return err
			}
			defer c.Close()
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
			if doc.Owner != "" || doc.OwnerShort != "" {
				// Start from the current User so fields the file lacks
				// (is_licensed and the rest) are kept, as owner set does.
				cur := st.Self().GetUser()
				owner := &pb.User{}
				if cur != nil {
					owner = proto.Clone(cur).(*pb.User)
				}
				if doc.Owner != "" {
					owner.LongName = doc.Owner
				}
				if doc.OwnerShort != "" {
					owner.ShortName = doc.OwnerShort
				}
				if owner.LongName == "" || owner.ShortName == "" {
					return errors.New("the node did not report its current owner; the backup must set both owner and owner_short")
				}
				if owner.LongName != cur.GetLongName() || owner.ShortName != cur.GetShortName() {
					changes = append(changes, device.Change{Owner: owner})
				}
			}
			if l := doc.Location; l != nil {
				// Python backups carry the GPS position too; only a fixed
				// position is meant to be restored.
				if !cfg.GetPosition().GetFixedPosition() {
					fmt.Fprintln(a.Stderr, "note: location ignored (position.fixed_position is false in this backup)")
				} else {
					changes = append(changes, device.Change{FixedPosition: &pb.Position{
						LatitudeI:  proto.Int32(int32(math.Round(l.Lat * 1e7))),
						LongitudeI: proto.Int32(int32(math.Round(l.Lon * 1e7))),
						Altitude:   proto.Int32(l.Alt),
					}})
				}
			}
			for _, k := range skipped {
				fmt.Fprintf(a.Stderr, "skipped: %s (%s)\n", k.Path, k.Reason)
			}
			for _, k := range doc.Unsupported {
				fmt.Fprintf(a.Stderr, "not supported, ignored: %s\n", k)
			}
			return applyChanges(cmd, a, c, n, st, changes)
		},
	}
	cmd.Flags().BoolVar(&keys, "keys", false, "also apply security.private_key and security.public_key")
	return cmd
}

// writeFileAtomic writes data to path with mode 0600. The temp file is created
// 0600 in the same directory and renamed over path, so the private key in a
// backup is never readable by others and a failure leaves no partial file.
func writeFileAtomic(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".meshtastik-export-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
