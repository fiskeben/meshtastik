// Package channelurl encodes and decodes Meshtastic channel-set URLs.
package channelurl

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// Prefix starts every channel URL; the ChannelSet follows the '#'.
const Prefix = "https://meshtastic.org/e/#"

// Encode builds the URL for the PRIMARY then SECONDARY channels (index order)
// and the LoRa config.
func Encode(channels [8]*pb.Channel, lora *pb.Config_LoRaConfig) (string, error) {
	set := &pb.ChannelSet{LoraConfig: lora}
	for _, role := range []pb.Channel_Role{pb.Channel_PRIMARY, pb.Channel_SECONDARY} {
		for _, c := range channels {
			if c.GetRole() == role {
				s := c.GetSettings()
				if s == nil {
					s = &pb.ChannelSettings{}
				}
				set.Settings = append(set.Settings, s)
			}
		}
	}
	if len(set.Settings) == 0 {
		return "", errors.New("no channels to encode")
	}
	raw, err := proto.Marshal(set)
	if err != nil {
		return "", err
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// Decode parses a channel URL (the text after '#' is what matters). It
// tolerates '=' padding, the standard base64 alphabet and a "?add=true" suffix.
func Decode(url string) (*pb.ChannelSet, error) {
	i := strings.LastIndex(url, "#")
	if i < 0 {
		return nil, errors.New("not a channel URL: missing '#'")
	}
	frag := url[i+1:]
	if j := strings.Index(frag, "?"); j >= 0 {
		frag = frag[:j]
	}
	frag = strings.TrimRight(strings.TrimSpace(frag), "=")
	frag = strings.NewReplacer("+", "-", "/", "_").Replace(frag)
	if frag == "" {
		return nil, errors.New("channel URL has no data after '#'")
	}
	raw, err := base64.RawURLEncoding.DecodeString(frag)
	if err != nil {
		return nil, fmt.Errorf("channel URL is not valid base64: %w", err)
	}
	set := &pb.ChannelSet{}
	if err := proto.Unmarshal(raw, set); err != nil {
		return nil, fmt.Errorf("channel URL is not a channel set: %w", err)
	}
	if len(set.Settings) == 0 {
		return nil, errors.New("channel URL contains no channels")
	}
	return set, nil
}

// ToChannels lays the set out as device channels: [0] PRIMARY, then
// SECONDARY, the rest DISABLED. At most 8 channels fit.
func ToChannels(set *pb.ChannelSet) [8]*pb.Channel {
	var out [8]*pb.Channel
	for i := range out {
		out[i] = &pb.Channel{Index: int32(i), Role: pb.Channel_DISABLED}
		if i < len(set.GetSettings()) {
			out[i].Role = pb.Channel_SECONDARY
			if i == 0 {
				out[i].Role = pb.Channel_PRIMARY
			}
			out[i].Settings = set.Settings[i]
		}
	}
	return out
}
