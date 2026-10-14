package family

import (
	"bytes"
	"fmt"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

// One config dialect spoken through plugin channels
type dialectProbe struct {
	query   string
	body    []byte
	answer  string
	reshape func(reply []byte) ([]byte, error)
}

// Probes each dialect the era defines
func dialectProbes(protocol int32) []dialectProbe {
	switch {
	case protocol == 765:
		return []dialectProbe{{
			query:   "neoforge:register",
			body:    []byte{0, 0},
			answer:  "neoforge:network",
			reshape: reshapeSetsDecl,
		}}
	case protocol >= 766:
		return []dialectProbe{{
			query:   "neoforge:register",
			body:    []byte{0},
			answer:  "neoforge:network",
			reshape: reshapeMapDecl,
		}}
	}
	return nil
}

// Component fields of one paired set entry
func readSetComponent(rd *bytes.Reader) (string, bool, string, error) {
	id, err := packet.ReadString(rd)
	if err != nil {
		return "", false, "", err
	}
	hasVer, err := packet.ReadBool(rd)
	if err != nil {
		return "", false, "", err
	}
	ver := ""
	if hasVer {
		if ver, err = packet.ReadString(rd); err != nil {
			return "", false, "", err
		}
	}
	if err := skipFlowAndOptional(rd); err != nil {
		return "", false, "", err
	}
	return id, hasVer, ver, nil
}

// Eats the flow ordinal and optional flag
func skipFlowAndOptional(rd *bytes.Reader) error {
	hasFlow, err := packet.ReadBool(rd)
	if err != nil {
		return err
	}
	if hasFlow {
		if _, err := mcproto.ReadVarInt(rd); err != nil {
			return err
		}
	}
	_, err = packet.ReadBool(rd)
	return err
}

// Replays a paired set declaration as accepted
func reshapeSetsDecl(reply []byte) ([]byte, error) {
	rd := bytes.NewReader(reply)
	var out bytes.Buffer
	for range 2 {
		n, err := mcproto.ReadVarInt(rd)
		if err != nil {
			return nil, err
		}
		mcproto.WriteVarInt(&out, n)
		for range n {
			id, hasVer, ver, err := readSetComponent(rd)
			if err != nil {
				return nil, err
			}
			packet.WriteString(&out, id)
			packet.WriteBool(&out, hasVer)
			if hasVer {
				packet.WriteString(&out, ver)
			}
		}
	}
	if rd.Len() != 0 {
		return nil, fmt.Errorf("declaration left %d trailing bytes", rd.Len())
	}
	return out.Bytes(), nil
}

// Replays a keyed map declaration as accepted
func reshapeMapDecl(reply []byte) ([]byte, error) {
	rd := bytes.NewReader(reply)
	var out bytes.Buffer
	size, err := mcproto.ReadVarInt(rd)
	if err != nil {
		return nil, err
	}
	mcproto.WriteVarInt(&out, size)
	for range size {
		proto, err := mcproto.ReadVarInt(rd)
		if err != nil {
			return nil, err
		}
		mcproto.WriteVarInt(&out, proto)
		count, err := mcproto.ReadVarInt(rd)
		if err != nil {
			return nil, err
		}
		mcproto.WriteVarInt(&out, count)
		for range count {
			id, err := packet.ReadString(rd)
			if err != nil {
				return nil, err
			}
			ver, err := packet.ReadString(rd)
			if err != nil {
				return nil, err
			}
			if err := skipFlowAndOptional(rd); err != nil {
				return nil, err
			}
			packet.WriteString(&out, id)
			packet.WriteString(&out, id)
			packet.WriteString(&out, ver)
		}
	}
	if rd.Len() != 0 {
		return nil, fmt.Errorf("declaration left %d trailing bytes", rd.Len())
	}
	return out.Bytes(), nil
}
