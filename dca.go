package dca

import (
	"encoding/binary"
	"io"
	"log"
	"time"
)

// Define constants
const (
	// The current version of the DCA format
	FormatVersion int8 = 1

	// The current version of the DCA program
	LibraryVersion string = "0.0.4"

	// The URL to the GitHub repository of DCA
	GitHubRepositoryURL string = "https://github.com/jonas747/dca"
)

type OpusReader interface {
	OpusFrame() (frame []byte, err error)
	FrameDuration() time.Duration
}

var Logger *log.Logger

// logln logs to assigned logger or standard logger
func logln(s ...interface{}) {
	if Logger != nil {
		Logger.Println(s...)
		return
	}

	log.Println(s...)
}

// logln logs to assigned logger or standard logger
func logf(format string, a ...interface{}) {
	if Logger != nil {
		Logger.Printf(format, a...)
		return
	}
	log.Printf(format, a...)
}

// DecodeFrame decodes a dca frame from an io.Reader and returns the raw opus audio ready to be sent to discord
func DecodeFrame(r io.Reader) (frame []byte, err error) {
	var size int16
	err = binary.Read(r, binary.LittleEndian, &size)
	if err != nil {
		return
	}

	frame = make([]byte, size)
	err = binary.Read(r, binary.LittleEndian, &frame)
	return
}
