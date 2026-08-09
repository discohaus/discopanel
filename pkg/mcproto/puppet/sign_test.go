package puppet

import (
	"bytes"
	"testing"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/family"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

// Sign entity nbt shaped like one wire update
func signUpdateNBT(lines [4]string, jsonLines bool) packet.Tag {
	messages := packet.NBTList{}
	for _, line := range lines {
		text := line
		if jsonLines {
			text = `{"text":"` + line + `"}`
		}
		messages.Elem = append(messages.Elem, packet.NBTString(text))
	}
	return packet.NBTCompound{
		{Name: "is_waxed", Tag: packet.NBTByte(1)},
		{Name: "front_text", Tag: packet.NBTCompound{
			{Name: "has_glowing_text", Tag: packet.NBTByte(1)},
			{Name: "color", Tag: packet.NBTString("cyan")},
			{Name: "messages", Tag: messages},
		}},
	}
}

// Sign updates surface as plain text events
func TestPuppetSignTextEvent(t *testing.T) {
	for _, protocol := range []int32{766, 772} {
		c := &Client{
			protocol: protocol,
			ids:      family.ModernIDsFor(protocol),
			events:   make(chan family.Event, 4),
			entities: map[int32]family.Pos{},
		}

		var frame bytes.Buffer
		mcproto.WriteVarInt(&frame, mcproto.VarInt(c.ids.BlockEntityData))
		packet.WriteNum(&frame, packet.PositionNew(3, -57, -14))
		mcproto.WriteVarInt(&frame, family.ModernSignEntity)
		lines := [4]string{"World", "● 2 online", "ꜱᴛᴇᴘ ᴛʜʀᴏᴜɢʜ", "1.21.8"}
		if err := packet.WriteNetworkNBT(&frame, signUpdateNBT(lines, protocol < 770)); err != nil {
			t.Fatalf("nbt write failed %v", err)
		}
		c.handleFrame(frame.Bytes())

		select {
		case ev := <-c.events:
			sign, ok := ev.(family.EvSignText)
			if !ok {
				t.Fatalf("protocol %d event %+v", protocol, ev)
			}
			if sign.X != 3 || sign.Y != -57 || sign.Z != -14 {
				t.Fatalf("protocol %d position %+v", protocol, sign)
			}
			if sign.Lines != lines {
				t.Fatalf("protocol %d lines %q", protocol, sign.Lines)
			}
		default:
			t.Fatalf("protocol %d dropped the sign update", protocol)
		}
	}
}

// Foreign block entities pass without events
func TestPuppetIgnoresOtherEntities(t *testing.T) {
	c := &Client{
		protocol: 772,
		ids:      family.ModernIDsFor(772),
		events:   make(chan family.Event, 4),
		entities: map[int32]family.Pos{},
	}
	var frame bytes.Buffer
	mcproto.WriteVarInt(&frame, mcproto.VarInt(c.ids.BlockEntityData))
	packet.WriteNum(&frame, packet.PositionNew(0, -62, 0))
	mcproto.WriteVarInt(&frame, 14)
	packet.WriteNetworkNBT(&frame, packet.NBTCompound{})
	c.handleFrame(frame.Bytes())

	select {
	case ev := <-c.events:
		t.Fatalf("beacon leaked event %+v", ev)
	default:
	}
}
