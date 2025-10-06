package main

import (
	"encoding/binary"
	"io"
)

type MsgType byte

const (
	MsgOpen  MsgType = 1
	MsgData  MsgType = 2
	MsgClose MsgType = 3
)

type Frame struct {
	Type   MsgType
	ConnID uint32
	Data   []byte
}

func encodeFrame(f Frame) []byte {
	buf := make([]byte, 1+4+4+len(f.Data))
	buf[0] = byte(f.Type)
	binary.BigEndian.PutUint32(buf[1:5], f.ConnID)
	binary.BigEndian.PutUint32(buf[5:9], uint32(len(f.Data)))
	copy(buf[9:], f.Data)
	return buf
}

func decodeFrame(r io.Reader) (*Frame, error) {
	hdr := make([]byte, 9)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	t := MsgType(hdr[0])
	connID := binary.BigEndian.Uint32(hdr[1:5])
	length := binary.BigEndian.Uint32(hdr[5:9])
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	return &Frame{t, connID, data}, nil
}
