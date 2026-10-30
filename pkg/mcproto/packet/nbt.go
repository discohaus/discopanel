package packet

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// Nbt tag type ids
const (
	tagEnd       = 0
	tagByte      = 1
	tagShort     = 2
	tagInt       = 3
	tagLong      = 4
	tagFloat     = 5
	tagDouble    = 6
	tagByteArray = 7
	tagString    = 8
	tagList      = 9
	tagCompound  = 10
	tagIntArray  = 11
	tagLongArray = 12
)

// One nbt value of any tag type
type Tag interface {
	typeID() byte
	writePayload(w io.Writer) error
}

type NBTByte int8
type NBTShort int16
type NBTInt int32
type NBTLong int64
type NBTFloat float32
type NBTDouble float64
type NBTString string
type NBTByteArray []byte
type NBTIntArray []int32
type NBTLongArray []int64

// Ordered compound entries keep output deterministic
type NBTField struct {
	Name string
	Tag  Tag
}

type NBTCompound []NBTField

// Uniform element list
type NBTList struct {
	Elem []Tag
}

func (NBTByte) typeID() byte      { return tagByte }
func (NBTShort) typeID() byte     { return tagShort }
func (NBTInt) typeID() byte       { return tagInt }
func (NBTLong) typeID() byte      { return tagLong }
func (NBTFloat) typeID() byte     { return tagFloat }
func (NBTDouble) typeID() byte    { return tagDouble }
func (NBTString) typeID() byte    { return tagString }
func (NBTByteArray) typeID() byte { return tagByteArray }
func (NBTIntArray) typeID() byte  { return tagIntArray }
func (NBTLongArray) typeID() byte { return tagLongArray }
func (NBTCompound) typeID() byte  { return tagCompound }
func (NBTList) typeID() byte      { return tagList }

func (v NBTByte) writePayload(w io.Writer) error {
	_, err := w.Write([]byte{byte(v)})
	return err
}

func (v NBTShort) writePayload(w io.Writer) error {
	return binary.Write(w, binary.BigEndian, int16(v))
}

func (v NBTInt) writePayload(w io.Writer) error {
	return binary.Write(w, binary.BigEndian, int32(v))
}

func (v NBTLong) writePayload(w io.Writer) error {
	return binary.Write(w, binary.BigEndian, int64(v))
}

func (v NBTFloat) writePayload(w io.Writer) error {
	return binary.Write(w, binary.BigEndian, math.Float32bits(float32(v)))
}

func (v NBTDouble) writePayload(w io.Writer) error {
	return binary.Write(w, binary.BigEndian, math.Float64bits(float64(v)))
}

func (v NBTString) writePayload(w io.Writer) error {
	if len(v) > math.MaxUint16 {
		return fmt.Errorf("nbt string too long: %d", len(v))
	}
	if err := binary.Write(w, binary.BigEndian, uint16(len(v))); err != nil {
		return err
	}
	_, err := w.Write([]byte(v))
	return err
}

func (v NBTByteArray) writePayload(w io.Writer) error {
	if err := binary.Write(w, binary.BigEndian, int32(len(v))); err != nil {
		return err
	}
	_, err := w.Write(v)
	return err
}

func (v NBTIntArray) writePayload(w io.Writer) error {
	if err := binary.Write(w, binary.BigEndian, int32(len(v))); err != nil {
		return err
	}
	for _, n := range v {
		if err := binary.Write(w, binary.BigEndian, n); err != nil {
			return err
		}
	}
	return nil
}

func (v NBTLongArray) writePayload(w io.Writer) error {
	if err := binary.Write(w, binary.BigEndian, int32(len(v))); err != nil {
		return err
	}
	for _, n := range v {
		if err := binary.Write(w, binary.BigEndian, n); err != nil {
			return err
		}
	}
	return nil
}

func (v NBTCompound) writePayload(w io.Writer) error {
	for _, field := range v {
		if _, err := w.Write([]byte{field.Tag.typeID()}); err != nil {
			return err
		}
		if err := NBTString(field.Name).writePayload(w); err != nil {
			return err
		}
		if err := field.Tag.writePayload(w); err != nil {
			return err
		}
	}
	_, err := w.Write([]byte{tagEnd})
	return err
}

func (v NBTList) writePayload(w io.Writer) error {
	elemType := byte(tagEnd)
	if len(v.Elem) > 0 {
		elemType = v.Elem[0].typeID()
	}
	if _, err := w.Write([]byte{elemType}); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, int32(len(v.Elem))); err != nil {
		return err
	}
	for _, e := range v.Elem {
		if e.typeID() != elemType {
			return fmt.Errorf("mixed nbt list element types")
		}
		if err := e.writePayload(w); err != nil {
			return err
		}
	}
	return nil
}

// Writes a named root tag the classic way
func WriteNBT(w io.Writer, name string, root Tag) error {
	if _, err := w.Write([]byte{root.typeID()}); err != nil {
		return err
	}
	if err := NBTString(name).writePayload(w); err != nil {
		return err
	}
	return root.writePayload(w)
}

// Writes a nameless root tag the network way
func WriteNetworkNBT(w io.Writer, root Tag) error {
	if _, err := w.Write([]byte{root.typeID()}); err != nil {
		return err
	}
	return root.writePayload(w)
}
