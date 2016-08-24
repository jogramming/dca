package dca

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strconv"
)

var (
	ErrNotDCA = errors.New("DCA Magic header not found, either not dca or raw dca frames")
)

type Decoder struct {
	Metadata      *Metadata
	FormatVersion int
	r             io.Reader
}

// NewDecoder returns a new dca decoder, and reads the first metadata frame
func NewDecoder(r io.Reader) *Decoder {
	decoder := &Decoder{
		r: r,
	}

	return decoder
}

// ReadMetadata reads the first metadata frame
func (d *Decoder) ReadMetadata() error {
	fingerprint := make([]byte, 4)
	_, err := d.r.Read(fingerprint)
	if err != nil {
		return err
	}

	if string(fingerprint[:3]) != "DCA" {
		return ErrNotDCA
	}

	// Read the format version
	version, err := strconv.ParseInt(string(fingerprint[3:]), 10, 32)
	if err != nil {
		return err
	}
	d.FormatVersion = int(version)

	// The length of the metadata
	var metaLen int32
	err = binary.Read(d.r, binary.LittleEndian, &metaLen)
	if err != nil {
		return err
	}

	// Read in the metadata itself
	jsonBuf := make([]byte, metaLen)
	err = binary.Read(d.r, binary.LittleEndian, &jsonBuf)
	if err != nil {
		return err
	}

	// And unmarshal it
	var metadata *Metadata
	err = json.Unmarshal(jsonBuf, &metadata)
	d.Metadata = metadata
	return err
}

// OpusFrame returns the next audio frame (without the prefixed length)
func (d *Decoder) OpusFrame() (frame []byte, err error) {
	frame, err = DecodeFrame(d.r)
	return
}
