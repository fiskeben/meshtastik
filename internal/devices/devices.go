// Package devices keeps the named connection targets and the current device
// that commands use when no --port, --host, --ble or --device is given.
package devices

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"sigs.k8s.io/yaml"
)

// Target is one node connection: exactly one field is set.
type Target struct {
	Port string `json:"port,omitempty"`
	Host string `json:"host,omitempty"`
	BLE  string `json:"ble,omitempty"`
}

// IsZero reports whether no field is set.
func (t Target) IsZero() bool { return t == Target{} }

// String is the target as daemon status shows it, such as tcp:10.0.0.2.
func (t Target) String() string {
	switch {
	case t.Port != "":
		return "serial:" + t.Port
	case t.Host != "":
		return "tcp:" + t.Host
	case t.BLE != "":
		return "ble:" + t.BLE
	}
	return ""
}

// Validate checks that exactly one field is set.
func (t Target) Validate() error {
	n := 0
	for _, s := range []string{t.Port, t.Host, t.BLE} {
		if s != "" {
			n++
		}
	}
	if n != 1 {
		return errors.New("a device needs exactly one of port, host or ble")
	}
	return nil
}

// Current is the device commands use by default: a named device, or a
// target saved without a name.
type Current struct {
	Device string `json:"device,omitempty"`
	Target
}

// File is the devices file.
type File struct {
	Current *Current          `json:"current,omitempty"`
	Devices map[string]Target `json:"devices,omitempty"`
}

// Names returns the device names in order.
func (f *File) Names() []string {
	names := make([]string, 0, len(f.Devices))
	for n := range f.Devices {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Lookup returns the named device, or an error naming the known ones.
func (f *File) Lookup(name string) (Target, error) {
	if t, ok := f.Devices[name]; ok {
		return t, nil
	}
	if len(f.Devices) == 0 {
		return Target{}, fmt.Errorf("no device named %q; add one with 'meshtastik device add'", name)
	}
	return Target{}, fmt.Errorf("no device named %q; known devices: %v", name, f.Names())
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// CheckName refuses names that would read as flags or targets.
func CheckName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("device name %q: use letters, digits, '.', '_' and '-', starting with a letter or digit", name)
	}
	return nil
}

// Path is where the devices file lives: $MESHTASTIK_DEVICES, else
// devices.yaml in the user's config directory under meshtastik.
func Path() string {
	if p := os.Getenv("MESHTASTIK_DEVICES"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "meshtastik", "devices.yaml")
}

// Load reads the file at path; a missing file is an empty one.
func Load(path string) (*File, error) {
	f := &File{}
	if path == "" {
		return f, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.UnmarshalStrict(b, f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c := f.Current; c != nil && c.Device != "" && !c.Target.IsZero() {
		return nil, fmt.Errorf("%s: current names a device and a target; keep one", path)
	}
	return f, nil
}

// Save writes f to path, replacing the file in one step.
func Save(path string, f *File) error {
	if path == "" {
		return errors.New("no place for the devices file: the user config directory is unknown; set MESHTASTIK_DEVICES")
	}
	b, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".devices-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
