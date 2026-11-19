package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
	"tinygo.org/x/bluetooth"
)

// Meshtastic GATT UUIDs.
const (
	bleServiceUUID   = "6ba1b218-15a8-461f-9fa8-5dcae273eafd"
	bleToRadioUUID   = "f75c76d2-129e-4dad-a1dd-7866124401e7"
	bleFromRadioUUID = "2c55e69e-4993-11ed-b878-0242ac120002"
	bleFromNumUUID   = "ed9da18c-a800-4f66-a670-aa7547e34453"

	bleScanCap = 10 * time.Second
)

// BLEDevice is a device seen while scanning. On macOS Address is a
// CoreBluetooth UUID rather than a MAC address.
type BLEDevice struct {
	Name, Address string
	RSSI          int16
}

// matchDevice reports whether d is the device named or addressed by want
// (case-insensitive, exact).
func matchDevice(d BLEDevice, want string) bool {
	return want != "" && (strings.EqualFold(d.Name, want) || strings.EqualFold(d.Address, want))
}

// bleLink is the GATT surface bleConn needs.
type bleLink interface {
	WriteToRadio(p []byte) error
	// ReadFromRadio reads one FromRadio into buf; 0 bytes means the queue is empty.
	ReadFromRadio(buf []byte) (int, error)
}

// bleConn speaks the Meshtastic BLE protocol: raw (unframed) protobufs,
// written to ToRadio and drained from FromRadio after each FromNum notification.
type bleConn struct {
	link bleLink
	log  io.Writer

	drainMu sync.Mutex // serialises drains
	msgs    chan *pb.FromRadio
	done    chan struct{}
	once    sync.Once
	cause   error // set before done is closed
	onClose func()
}

func newBLEConn(l bleLink, log io.Writer) *bleConn {
	if log == nil {
		log = io.Discard
	}
	return &bleConn{link: l, log: log, msgs: make(chan *pb.FromRadio, 64), done: make(chan struct{})}
}

// shutdown closes the conn once, recording why.
func (c *bleConn) shutdown(cause error) {
	c.once.Do(func() {
		c.cause = cause
		close(c.done)
		if c.onClose != nil {
			c.onClose()
		}
	})
}

// notify is called on FromNum notifications; the drain runs asynchronously so
// the BLE callback goroutine is never blocked.
func (c *bleConn) notify() { go c.drain() }

// drain reads FromRadio until the radio reports its queue empty.
func (c *bleConn) drain() {
	c.drainMu.Lock()
	defer c.drainMu.Unlock()
	buf := make([]byte, 512)
	for {
		select {
		case <-c.done:
			return
		default:
		}
		n, err := c.link.ReadFromRadio(buf)
		if err != nil {
			c.shutdown(fmt.Errorf("read FromRadio: %w", err))
			return
		}
		if n == 0 {
			return
		}
		var m pb.FromRadio
		if err := proto.Unmarshal(buf[:n], &m); err != nil {
			fmt.Fprintf(c.log, "skipping undecodable FromRadio (%d bytes): %v\n", n, err)
			continue
		}
		select {
		case c.msgs <- &m:
		case <-c.done:
			return
		}
	}
}

func (c *bleConn) Send(ctx context.Context, m *pb.ToRadio) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-c.done:
		return fmt.Errorf("%w: %v", ErrClosed, c.cause)
	default:
	}
	p, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	if err := c.link.WriteToRadio(p); err != nil {
		c.shutdown(fmt.Errorf("write ToRadio: %w", err))
		return fmt.Errorf("%w: %v", ErrClosed, err)
	}
	c.drain()
	return nil
}

func (c *bleConn) Recv(ctx context.Context) (*pb.FromRadio, error) {
	select {
	case m := <-c.msgs:
		return m, nil
	case <-c.done:
		select { // deliver anything already queued first
		case m := <-c.msgs:
			return m, nil
		default:
		}
		return nil, fmt.Errorf("%w: %v", ErrClosed, c.cause)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *bleConn) Kind() Kind { return KindBLE }

func (c *bleConn) Close() error {
	c.shutdown(errors.New("closed locally"))
	return nil
}

// gattLink is the real bleLink over tinygo characteristics.
type gattLink struct {
	toRadio, fromRadio bluetooth.DeviceCharacteristic
}

func (g *gattLink) WriteToRadio(p []byte) error {
	_, err := g.toRadio.Write(p)
	return err
}

func (g *gattLink) ReadFromRadio(buf []byte) (int, error) { return g.fromRadio.Read(buf) }

var enableOnce sync.Once
var enableErr error

func adapter() (*bluetooth.Adapter, error) {
	enableOnce.Do(func() { enableErr = bluetooth.DefaultAdapter.Enable() })
	if enableErr != nil {
		return nil, fmt.Errorf("enable bluetooth: %w", enableErr)
	}
	return bluetooth.DefaultAdapter, nil
}

// scanFor scans until stop (called from the scan callback) returns true, the
// duration elapses or ctx ends.
func scanFor(ctx context.Context, d time.Duration, visit func(r bluetooth.ScanResult) (stop bool)) error {
	a, err := adapter()
	if err != nil {
		return err
	}
	finished := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
		case <-finished:
			return
		}
		_ = a.StopScan()
	}()
	var stopOnce sync.Once
	err = a.Scan(func(a *bluetooth.Adapter, r bluetooth.ScanResult) {
		if visit(r) {
			stopOnce.Do(func() { _ = a.StopScan() })
		}
	})
	close(finished)
	<-stopped
	return err
}

// ScanBLE scans for d and returns the Meshtastic devices seen (advertising the
// Meshtastic service or named "Meshtastic*"), one per address with the
// strongest RSSI.
func ScanBLE(ctx context.Context, d time.Duration) ([]BLEDevice, error) {
	svc, _ := bluetooth.ParseUUID(bleServiceUUID)
	best := map[string]BLEDevice{}
	var order []string
	var mu sync.Mutex
	err := scanFor(ctx, d, func(r bluetooth.ScanResult) bool {
		name := r.LocalName()
		if !r.HasServiceUUID(svc) && !strings.HasPrefix(strings.ToLower(name), "meshtastic") {
			return false
		}
		dev := BLEDevice{Name: name, Address: r.Address.String(), RSSI: r.RSSI}
		mu.Lock()
		defer mu.Unlock()
		old, seen := best[dev.Address]
		if !seen {
			order = append(order, dev.Address)
		} else {
			if dev.Name == "" {
				dev.Name = old.Name
			}
			if old.RSSI > dev.RSSI {
				dev.RSSI = old.RSSI
			}
		}
		best[dev.Address] = dev
		return false
	})
	if err != nil {
		return nil, err
	}
	mu.Lock()
	defer mu.Unlock()
	out := make([]BLEDevice, 0, len(order))
	for _, a := range order {
		out = append(out, best[a])
	}
	return out, nil
}

// DialBLE finds the device whose name or address is nameOrAddr, connects and
// returns a Conn over the Meshtastic GATT service.
func DialBLE(ctx context.Context, nameOrAddr string, log io.Writer) (Conn, error) {
	if log == nil {
		log = io.Discard
	}
	svc, _ := bluetooth.ParseUUID(bleServiceUUID)
	var found bluetooth.ScanResult
	var ok bool
	err := scanFor(ctx, bleScanCap, func(r bluetooth.ScanResult) bool {
		if ok {
			return true
		}
		if len(r.ServiceUUIDs()) > 0 && !r.HasServiceUUID(svc) {
			return false
		}
		if !matchDevice(BLEDevice{Name: r.LocalName(), Address: r.Address.String()}, nameOrAddr) {
			return false
		}
		found, ok = r, true
		return true
	})
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	if !ok {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("BLE device %q not found", nameOrAddr)
	}
	a, err := adapter()
	if err != nil {
		return nil, err
	}
	dev, err := a.Connect(found.Address, bluetooth.ConnectionParams{})
	if err != nil {
		return nil, fmt.Errorf("connect %q: %w", nameOrAddr, err)
	}
	fail := func(err error) (Conn, error) {
		_ = dev.Disconnect()
		return nil, err
	}
	services, err := dev.DiscoverServices([]bluetooth.UUID{svc})
	if err != nil || len(services) == 0 {
		return fail(fmt.Errorf("Meshtastic service %s not found on %q: %v", bleServiceUUID, nameOrAddr, err))
	}
	chars := map[string]bluetooth.DeviceCharacteristic{}
	for name, u := range map[string]string{"ToRadio": bleToRadioUUID, "FromRadio": bleFromRadioUUID, "FromNum": bleFromNumUUID} {
		cu, _ := bluetooth.ParseUUID(u)
		cs, err := services[0].DiscoverCharacteristics([]bluetooth.UUID{cu})
		if err != nil || len(cs) == 0 {
			return fail(fmt.Errorf("characteristic %s (%s) not found on %q: %v", name, u, nameOrAddr, err))
		}
		chars[name] = cs[0]
	}
	fromNum := chars["FromNum"]
	c := newBLEConn(&gattLink{toRadio: chars["ToRadio"], fromRadio: chars["FromRadio"]}, log)
	c.onClose = func() { _ = dev.Disconnect() }
	if err := fromNum.EnableNotifications(func([]byte) { c.notify() }); err != nil {
		return fail(fmt.Errorf("enable FromNum notifications: %w", err))
	}
	c.drain()
	return c, nil
}
