package dca

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

var (
	ErrClosed = errors.New("Frame channel is closed (no more frames)")
)

// AudioApplication is an application profile for opus encoding
type AudioApplication string

var (
	AudioApplicationVoip     AudioApplication = "voip"     // Favor improved speech intelligibility.
	AudioApplicationAudio    AudioApplication = "audio"    // Favor faithfulness to the input
	AudioApplicationLowDelay AudioApplication = "lowdelay" // Restrict to only the lowest delay modes.
)

// EncodeOptions is a set of options for encoding dca
type EncodeOptions struct {
	Volume        int              // change audio volume (256=normal)
	Channels      int              // audio channels
	FrameRate     int              // audio sampling rate (ex 48000)
	FrameDuration int              // audio frame duration can be 20, 40, or 60 (ms)
	Bitrate       int              // audio encoding bitrate in kb/s can be 8 - 128
	RawOutput     bool             // Raw opus output (no metadata or magic bytes)
	Application   AudioApplication // Audio application
	CoverFormat   string           // Format the cover art will be encoded with (ex "jpeg)
}

// Validate returns an error if the options are not correct
func (opts *EncodeOptions) Validate() error {
	if opts.Volume < 0 || opts.Volume > 256 {
		return errors.New("Out of bounds volume (0-256)")
	}

	if opts.FrameDuration != 20 && opts.FrameDuration != 40 && opts.FrameDuration != 60 {
		return errors.New("Invalid FrameDuration")
	}

	return nil
}

// StdEncodeOptions is the standard options for encoding
var StdEncodeOptions = &EncodeOptions{
	Volume:        256,
	Channels:      2,
	FrameRate:     48000,
	FrameDuration: 20,
	Bitrate:       64,
	Application:   AudioApplicationAudio,
	RawOutput:     true,
}

// EncodeSession is an encoding session
type EncodeSession interface {
	Stop() error                          // Stops the encoding session
	Progress() time.Duration              // Returns the number of seconds that has elapsed
	ReadFrame() (frame []byte, err error) // Retrieves a frame
	Running() bool                        // Wether its encoding or not
}

type encodeSession struct {
	sync.Mutex
	options    *EncodeOptions
	pipeReader io.Reader
	filePath   string

	running      bool
	started      time.Time
	frameChannel chan []byte
	process      *os.Process
}

// EncodedMem encodes data from memory
func EncodeMem(r io.Reader, options *EncodeOptions) (session EncodeSession) {
	s := &encodeSession{
		options:      options,
		pipeReader:   r,
		frameChannel: make(chan []byte),
	}
	go s.run()
	return s
}

// EncodeFile encodes the file in path
func EncodeFile(path string, options *EncodeOptions) (session EncodeSession) {
	s := &encodeSession{
		options:      options,
		filePath:     path,
		frameChannel: make(chan []byte),
	}
	go s.run()
	return s
}

func (e *encodeSession) run() {
	// Reset running state
	defer func() {
		e.Lock()
		e.running = false
		e.Unlock()
	}()

	e.Lock()
	e.running = true

	inFile := "pipe:0"
	if e.filePath != "" {
		inFile = e.filePath
	}

	if e.options == nil {
		e.options = StdEncodeOptions
	}

	// Launch ffmpeg with a variety of different fruits and goodies mixed togheter
	ffmpeg := exec.Command("ffmpeg", "-i", inFile, "-map", "0:a", "-acodec", "libopus", "-f", "data", "-sample_fmt", "s16", "-vbr", "off", "-compression_level", "10",
		"-vol", strconv.Itoa(e.options.Volume), "-ar", strconv.Itoa(e.options.FrameRate), "-ac", strconv.Itoa(e.options.Channels), "-b:a", strconv.Itoa(e.options.Bitrate*1000), "pipe:1")

	Logln(ffmpeg.Args)

	stdIn, err := ffmpeg.StdinPipe()
	if err != nil {
		e.Unlock()
		Logln("StdinPipe Error:", err)
	}

	stdout, err := ffmpeg.StdoutPipe()
	if err != nil {
		e.Unlock()
		Logln("StdoutPipe Error:", err)
		close(e.frameChannel)
		return
	}

	// Starts the ffmpeg command
	err = ffmpeg.Start()
	if err != nil {
		e.Unlock()
		Logln("RunStart Error:", err)
		close(e.frameChannel)
		return
	}

	e.started = time.Now()

	if e.pipeReader != nil {
		go e.writeStdin(stdIn)
	}

	e.process = ffmpeg.Process
	e.Unlock()

	e.readStdout(stdout)
	err = ffmpeg.Wait()
	if err != nil {
		Logln("Error waiting for ffmpeg:", err)
	}
}

// Writes to the pipe of ffmpeg
func (e *encodeSession) writeStdin(stdin io.WriteCloser) {
	_, err := io.Copy(stdin, e.pipeReader)
	if err != nil {
		Logln("io.Copy stdin Error:", err)
	}
	err = stdin.Close()
	if err != nil {
		Logln("stdin.Close Error:", err)
	}
}

func (e *encodeSession) readStdout(stdout io.ReadCloser) {
	defer close(e.frameChannel)

	for {
		// read data from ffmpeg stdout
		inBuf := make([]byte, (e.options.Bitrate*e.options.FrameDuration)/8)
		err := binary.Read(stdout, binary.LittleEndian, &inBuf)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return
		}

		if err != nil {
			Logln("error reading from ffmpeg stdout:", err)
			return
		}

		var framebuf bytes.Buffer

		// write frameheader
		opuslen := int16(len(inBuf))
		err = binary.Write(&framebuf, binary.LittleEndian, &opuslen)
		if err != nil {
			Logln("error writing output:", err)
			return
		}

		// write opus data to stdout
		err = binary.Write(&framebuf, binary.LittleEndian, &inBuf)
		if err != nil {
			Logln("error writing frame:", err)
			continue
		}

		// Add to framebuffer
		e.frameChannel <- framebuf.Bytes()
	}
}

// Implement the EncodeSession interface

// Stop Stops the encoding session
func (e *encodeSession) Stop() error {
	e.Lock()
	defer e.Unlock()
	if !e.running || e.process == nil {
		return errors.New("Not running")
	}

	err := e.process.Kill()
	return err
}

// Progress returns the duration of wich the encodesession has been running
func (e *encodeSession) Progress() time.Duration {
	e.Lock()
	elapsed := time.Since(e.started)
	e.Unlock()
	return elapsed
}

// ReadFrame blocks untill a frame is read or there are no more frames
func (e *encodeSession) ReadFrame() (frame []byte, err error) {
	frame = <-e.frameChannel
	if frame == nil {
		err = ErrClosed
	}
	return
}

// Running return true if running
func (e *encodeSession) Running() (running bool) {
	e.Lock()
	running = e.running
	e.Unlock()
	return
}
