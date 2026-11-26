package ircmesh

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"meshtastik/internal/device"
	"meshtastik/internal/irc"
)

func init() {
	commands["PRIVMSG"] = (*Server).cmdPrivmsg
	commands["NOTICE"] = func(*Server, *client, irc.Message) {} // never sent to the mesh
}

// sendJob is one IRC message on its way to the mesh.
type sendJob struct {
	to      uint32 // device.Broadcast for a channel
	channel int
	target  string // the IRC target, for notices
	parts   []string
	ids     []uint32
}

// sendQueueLen is how many messages may wait for the radio.
const sendQueueLen = 64

// notice is a NOTICE from the server to the IRC user.
func (s *Server) notice(target, text string) irc.Message {
	return irc.Message{Prefix: serverName, Command: "NOTICE", Params: []string{target, text}}
}

func (s *Server) cmdPrivmsg(c *client, m irc.Message) {
	if len(m.Params) < 1 {
		c.queue(s.numeric(c, "411", "No recipient given (PRIVMSG)"))
		return
	}
	if len(m.Params) < 2 || m.Params[1] == "" {
		c.queue(s.numeric(c, "412", "No text to send"))
		return
	}
	target, text := m.Params[0], irc.ToUTF8(m.Params[1])
	me := s.selfNick()
	if cmd, arg, ok := irc.ParseCTCP(text); ok {
		switch cmd {
		case "ACTION":
			text = "* " + me + " " + arg
		case "VERSION":
			c.queue(s.notice(me, irc.CTCP("VERSION", "ircmesh "+s.cfg.Version)))
			return
		default:
			return
		}
	}
	text = strings.TrimSpace(irc.StripFormatting(text))
	if text == "" {
		return
	}
	job := sendJob{to: device.Broadcast}
	if strings.HasPrefix(target, "#") {
		i, ok := s.channelIndex(target)
		if !ok {
			c.queue(s.numeric(c, "403", target, "No such channel"))
			return
		}
		job.channel, job.target = i, s.names[i]
	} else {
		num, ok := s.dir.lookup(target)
		switch {
		case !ok:
			c.queue(s.numeric(c, "401", target, "No such nick/channel"))
			return
		case num == s.self:
			c.queue(s.notice(me, "can't send a message to your own node"))
			return
		}
		job.to, job.target = num, s.dir.nick(num)
	}
	parts, err := splitText(text, maxTextBytes, maxParts)
	if err != nil {
		c.queue(s.notice(me, err.Error()+" — not sent"))
		return
	}
	if s.sendQ == nil || !s.up {
		c.queue(s.notice(me, "mesh link is down; message not sent"))
		return
	}
	job.parts = parts
	for _, p := range parts {
		id := device.NewPacketID()
		job.ids = append(job.ids, id)
		s.recent.add(id, p)
	}
	select {
	case s.sendQ <- job:
	default:
		c.queue(s.notice(me, "too many messages waiting for the radio; message not sent"))
		return
	}
	for _, other := range s.registered() {
		if other == c {
			continue
		}
		for _, p := range parts {
			other.queue(irc.Message{Prefix: s.dir.mask(s.self), Command: "PRIVMSG", Params: []string{job.target, p}})
		}
	}
}

// closing reports whether ctx was cancelled (the link is being torn down),
// as opposed to its own deadline passing.
func closing(ctx context.Context) bool { return errors.Is(ctx.Err(), context.Canceled) }

// sender sends queued messages to m one packet at a time, at least
// SendInterval apart, until ctx ends or q is closed. Direct messages wait
// for their ack in the background.
func (s *Server) sender(ctx context.Context, m Mesh, q <-chan sendJob) {
	var last time.Time
	for {
		var job sendJob
		select {
		case <-ctx.Done():
			return
		case j, ok := <-q:
			if !ok {
				return
			}
			job = j
		}
		s.mu.Lock()
		up := s.up
		if !up {
			s.toAll(s.notice(s.selfNick(), "mesh link is down; message not sent"))
		}
		s.mu.Unlock()
		if !up {
			continue
		}
		for i, part := range job.parts {
			if wait := s.cfg.SendInterval - time.Since(last); wait > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
			last = time.Now()
			id := job.ids[i]
			if job.to != device.Broadcast {
				s.wg.Add(1)
				go func() {
					defer s.wg.Done()
					ctx, cancel := context.WithTimeout(ctx, s.cfg.DMTimeout)
					defer cancel()
					if err := m.SendTextID(ctx, id, part, job.to, 0, true); err != nil && !errors.Is(err, context.Canceled) && !closing(ctx) {
						s.mu.Lock()
						s.toAll(s.notice(s.selfNick(), fmt.Sprintf("message to %s not delivered (%v): %q", job.target, err, quote(part))))
						s.mu.Unlock()
					}
				}()
				continue
			}
			sctx, cancel := context.WithTimeout(ctx, s.cfg.DMTimeout)
			err := m.SendTextID(sctx, id, part, device.Broadcast, uint32(job.channel), false)
			cancel()
			if err != nil && ctx.Err() == nil {
				s.mu.Lock()
				s.toChannel(job.channel, s.notice(job.target, "not sent: "+err.Error()))
				s.mu.Unlock()
			}
		}
	}
}
