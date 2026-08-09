package puppet

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/family"
	"github.com/discohaus/discopanel/pkg/mcproto/mojang"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
	"github.com/discohaus/discopanel/pkg/mcproto/session"
)

// Join budget covering login config and first sync
const dialReadyTimeout = 25 * time.Second

// Everything a puppet needs to join the lobby
type Config struct {
	Addr       string
	Name       string
	Protocol   int32
	StateNames map[int32]string
}

// Headless lobby presence mirroring one legacy player
type Client struct {
	conn      net.Conn
	protocol  int32
	threshold int
	ids       *family.ModernIDs
	cfg       Config

	self     [16]byte
	entityID int32
	spawn    family.Pos
	pos      family.Pos

	events  chan family.Event
	ready   chan struct{}
	readyMu sync.Mutex
	isReady bool
	loopErr error
	closed  atomic.Bool

	writeMu  sync.Mutex
	entities map[int32]family.Pos
}

// Joins the lobby and waits until standing in it
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	ids := family.ModernIDsFor(cfg.Protocol)
	if ids == nil {
		return nil, fmt.Errorf("puppet can't speak protocol %d", cfg.Protocol)
	}

	host, portText, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("bad puppet address %q: %w", cfg.Addr, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, fmt.Errorf("bad puppet port %q: %w", portText, err)
	}

	d := net.Dialer{KeepAlive: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(dialReadyTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	conn.SetDeadline(deadline)

	id := mojang.OfflineUUID(cfg.Name)
	login, err := session.LoginAsClient(conn, cfg.Protocol, host, uint16(port), cfg.Name, id)
	if err != nil {
		conn.Close()
		return nil, err
	}

	c := &Client{
		conn:      conn,
		protocol:  cfg.Protocol,
		threshold: login.Threshold,
		ids:       ids,
		cfg:       cfg,
		self:      login.UUID,
		events:    make(chan family.Event, 256),
		ready:     make(chan struct{}),
		entities:  make(map[int32]family.Pos),
	}

	if err := c.runConfig(); err != nil {
		conn.Close()
		return nil, err
	}

	go c.readLoop()

	select {
	case <-c.ready:
		if c.loopErr != nil {
			conn.Close()
			return nil, c.loopErr
		}
		conn.SetDeadline(time.Time{})
		return c, nil
	case <-ctx.Done():
		conn.Close()
		return nil, ctx.Err()
	case <-time.After(time.Until(deadline)):
		conn.Close()
		return nil, fmt.Errorf("puppet never reached the lobby floor")
	}
}

// Normalized lobby happenings for the session
func (c *Client) Events() <-chan family.Event {
	return c.events
}

// Own entity id from the join
func (c *Client) EntityID() int32 {
	return c.entityID
}

// Where the lobby placed the puppet
func (c *Client) Spawn() family.Pos {
	return c.spawn
}

// Drops the lobby connection
func (c *Client) Close() {
	if c.closed.CompareAndSwap(false, true) {
		c.conn.Close()
	}
}

// Marks the puppet standing and unblocks the dial
func (c *Client) markReady() {
	c.readyMu.Lock()
	if !c.isReady {
		c.isReady = true
		c.spawn = c.pos
		close(c.ready)
	}
	c.readyMu.Unlock()
}

// Unblocks a dial stuck on a dead loop
func (c *Client) markFailed(err error) {
	c.readyMu.Lock()
	if !c.isReady {
		c.isReady = true
		c.loopErr = err
		close(c.ready)
	}
	c.readyMu.Unlock()
}

// Sends one framed body under the write lock
func (c *Client) send(body []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return packet.WriteFrameZ(c.conn, body, c.threshold)
}

// Walks the puppet to an absolute position
func (c *Client) Move(pos family.Pos) error {
	if c.closed.Load() {
		return fmt.Errorf("puppet closed")
	}
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(c.ids.PlayerPosRot))
	packet.WriteNum(&body, pos.X)
	packet.WriteNum(&body, pos.Y)
	packet.WriteNum(&body, pos.Z)
	packet.WriteNum(&body, pos.Yaw)
	packet.WriteNum(&body, pos.Pitch)
	if c.ids.WideTeleport {
		ground := byte(0)
		if pos.OnGround {
			ground = 0x01
		}
		body.WriteByte(ground)
	} else {
		packet.WriteBool(&body, pos.OnGround)
	}
	if err := c.send(body.Bytes()); err != nil {
		return err
	}
	c.pos = pos
	return nil
}

// Speaks one unsigned chat line as the player
func (c *Client) Chat(text string) error {
	if c.closed.Load() {
		return fmt.Errorf("puppet closed")
	}
	if len(text) > 256 {
		text = text[:256]
	}
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(c.ids.ChatSB))
	packet.WriteString(&body, text)
	packet.WriteNum(&body, time.Now().UnixMilli())
	packet.WriteNum(&body, int64(0))
	packet.WriteBool(&body, false)
	mcproto.WriteVarInt(&body, 0)
	body.Write([]byte{0, 0, 0})
	if c.ids.ChatChecksum {
		body.WriteByte(0)
	}
	return c.send(body.Bytes())
}

// Emits one event without wedging on a dead consumer
func (c *Client) emit(ev family.Event) {
	select {
	case c.events <- ev:
	default:
	}
}
