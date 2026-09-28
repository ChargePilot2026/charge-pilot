// Package dc589 implements the vendor's 5.8.9 binary TCP framing protocol.
// The wire format is EE, LEN, CMD, six session bytes, payload, XOR checksum.
package dc589

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

const StartByte byte = 0xEE
const HeaderSize = 8 // CMD + six-byte session + checksum, counted by LEN

var (
	ErrFrame  = errors.New("invalid 5.8.9 frame")
	ErrLength = errors.New("invalid 5.8.9 length")
	ErrSum    = errors.New("invalid 5.8.9 XOR checksum")
)

type Frame struct {
	Command byte
	Session [6]byte
	Data    []byte
}

func Encode(frame Frame) ([]byte, error) {
	length := HeaderSize + len(frame.Data)
	if length > 255 {
		return nil, ErrLength
	}
	out := make([]byte, 2+length)
	out[0], out[1], out[2] = StartByte, byte(length), frame.Command
	copy(out[3:9], frame.Session[:])
	copy(out[9:len(out)-1], frame.Data)
	out[len(out)-1] = checksum(out[1 : len(out)-1])
	return out, nil
}

func Decode(raw []byte) (Frame, error) {
	if len(raw) < 2+HeaderSize || raw[0] != StartByte {
		return Frame{}, ErrFrame
	}
	if raw[1] < HeaderSize || len(raw) != 2+int(raw[1]) {
		return Frame{}, ErrLength
	}
	if checksum(raw[1:len(raw)-1]) != raw[len(raw)-1] {
		return Frame{}, ErrSum
	}
	var session [6]byte
	copy(session[:], raw[3:9])
	return Frame{Command: raw[2], Session: session, Data: bytes.Clone(raw[9 : len(raw)-1])}, nil
}

// ReadFrame reads one complete frame from a TCP stream, where packet boundaries
// may not align with TCP reads. It intentionally rejects bad framing instead of
// silently scanning past bytes from an unauthenticated device.
func ReadFrame(reader *bufio.Reader) (Frame, error) {
	var head [2]byte
	if _, err := io.ReadFull(reader, head[:]); err != nil {
		return Frame{}, err
	}
	if head[0] != StartByte {
		return Frame{}, ErrFrame
	}
	if head[1] < HeaderSize {
		return Frame{}, ErrLength
	}
	raw := make([]byte, 2+int(head[1]))
	copy(raw[:2], head[:])
	if _, err := io.ReadFull(reader, raw[2:]); err != nil {
		return Frame{}, fmt.Errorf("read 5.8.9 frame: %w", err)
	}
	return Decode(raw)
}

func checksum(payload []byte) byte {
	var sum byte
	for _, b := range payload {
		sum ^= b
	}
	return sum
}
