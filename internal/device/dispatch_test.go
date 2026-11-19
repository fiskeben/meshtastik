package device

import (
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

func TestDispatchPacketKeepsLastHeardWithoutRxTime(t *testing.T) {
	c := &Client{state: newState(), opts: Options{}, waiters: map[uint32]chan *pb.MeshPacket{}}
	c.opts.defaults()
	c.state.Nodes[7] = &pb.NodeInfo{Num: 7, LastHeard: 100, Snr: 4}
	c.dispatch(nil, &pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: &pb.MeshPacket{From: 7}}})
	if n := c.State().Nodes[7]; n.LastHeard != 100 || n.Snr != 4 {
		t.Fatal(n)
	}
	c.dispatch(nil, &pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: &pb.MeshPacket{From: 7, RxTime: new(uint32(200)), RxSnr: 6}}})
	if n := c.State().Nodes[7]; n.LastHeard != 200 || n.Snr != 6 {
		t.Fatal(n)
	}
}

func TestDispatchIgnoresNilNodeInfo(t *testing.T) {
	c := &Client{state: newState(), waiters: map[uint32]chan *pb.MeshPacket{}}
	c.opts.defaults()
	c.dispatch(nil, &pb.FromRadio{PayloadVariant: &pb.FromRadio_NodeInfo{}})
	if len(c.State().Nodes) != 0 {
		t.Fatal(c.State().Nodes)
	}
}

func TestNewNonceSkipsSpecialValues(t *testing.T) {
	for range 1000 {
		if n := newNonce(); n == 0 || n == 69420 || n == 69421 {
			t.Fatal(n)
		}
	}
}
