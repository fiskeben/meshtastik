package device

import (
	"context"
	"fmt"
	"strconv"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

// NodeOp is a change to one entry of the node's node list.
type NodeOp int

const (
	RemoveNode NodeOp = iota
	Favorite
	Unfavorite
	Ignore
	Unignore
)

func (op NodeOp) String() string {
	return [...]string{"remove", "favorite", "unfavorite", "ignore", "unignore"}[op]
}

func (op NodeOp) admin(num uint32) *pb.AdminMessage {
	m := &pb.AdminMessage{}
	switch op {
	case RemoveNode:
		m.PayloadVariant = &pb.AdminMessage_RemoveByNodenum{RemoveByNodenum: num}
	case Favorite:
		m.PayloadVariant = &pb.AdminMessage_SetFavoriteNode{SetFavoriteNode: num}
	case Unfavorite:
		m.PayloadVariant = &pb.AdminMessage_RemoveFavoriteNode{RemoveFavoriteNode: num}
	case Ignore:
		m.PayloadVariant = &pb.AdminMessage_SetIgnoredNode{SetIgnoredNode: num}
	case Unignore:
		m.PayloadVariant = &pb.AdminMessage_RemoveIgnoredNode{RemoveIgnoredNode: num}
	}
	return m
}

// check compares s with the outcome op should have had on node num.
func (op NodeOp) check(s *State, num uint32) []Mismatch {
	path := fmt.Sprintf("node !%08x ", num)
	n, listed := s.Nodes[num]
	flag := func(name string, want, got bool) []Mismatch {
		if want == got {
			return nil
		}
		return []Mismatch{{Path: path + name, Want: strconv.FormatBool(want), Got: strconv.FormatBool(got)}}
	}
	switch op {
	case RemoveNode:
		if listed {
			return []Mismatch{{Path: path[:len(path)-1], Want: "absent", Got: "present"}}
		}
		return nil
	case Favorite, Unfavorite:
		return flag("favorite", op == Favorite, n.GetIsFavorite())
	}
	return flag("ignored", op == Ignore, n.GetIsIgnored())
}

// EditNodeDB applies op to node num on the connected node and verifies it
// by re-reading the node list.
func (c *Client) EditNodeDB(ctx context.Context, op NodeOp, num uint32) error {
	if num == c.State().MyInfo.GetMyNodeNum() {
		return fmt.Errorf("cannot %s the connected node itself", op)
	}
	if err := c.action(ctx, op.admin(num)); err != nil {
		return err
	}
	if err := c.Refresh(ctx); err != nil {
		return fmt.Errorf("%s sent but not verified: re-read the node list: %w", op, err)
	}
	if mm := op.check(c.State(), num); len(mm) > 0 {
		return &VerifyError{Mismatches: mm}
	}
	return nil
}

// ResetNodeDB clears the node list, keeping favourites if asked (nodes in a
// router or base role always keep them); the node reboots.
func (c *Client) ResetNodeDB(ctx context.Context, keepFavorites bool) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	if err := c.action(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_NodedbReset{NodedbReset: keepFavorites}}); err != nil {
		return err
	}
	if err := c.awaitNode(ctx); err != nil {
		return fmt.Errorf("node list reset sent; waiting for the node to come back: %w", err)
	}
	return nil
}
