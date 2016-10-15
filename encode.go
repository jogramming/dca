package dca

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonas747/ogg"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
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
	Volume           int              // change audio volume (256=normal)
	Channels         int              // audio channels
	FrameRate        int              // audio sampling rate (ex 48000)
	FrameDuration    int              // audio frame duration can be 20, 40, or 60 (ms)
	Bitrate          int              // audio encoding bitrate in kb/s can be 8 - 128
	PacketLoss       int              // expected packet loss percentage
	RawOutput        bool             // Raw opus output (no metadata or magic bytes)
	Application      AudioApplication // Audio application
	CoverFormat      string           // Format the cover art will be encoded with (ex "jpeg)
	CompressionLevel int              // Compression level, higher is better qualiy but slower encoding (0 - 10)
	BufferedFrames   int              // How big the frame buffer should be
	VBR              bool             // Wether vbr is used or not (variable bitrate)

	Comment string // Leave a comment in the metadata
}

func (e EncodeOptions) PCMFrameLen() int { // DCA needs this
	return 960 * e.Channels * (e.FrameDuration / 20)
}

// Validate returns an error if the options are not correct
func (opts *EncodeOptions) Validate() error {
	if opts.Volume < 0 || opts.Volume > 512 {
		return errors.New("Out of bounds volume (0-512)")
	}

	if opts.FrameDuration != 20 && opts.FrameDuration != 40 && opts.FrameDuration != 60 {
		return errors.New("Invalid FrameDuration")
	}

	if opts.PacketLoss < 0 || opts.PacketLoss > 100 {
		return errors.New("Invalid packet loss percentage")
	}

	return nil
}

// StdEncodeOptions is the standard options for encoding
var StdEncodeOptions = &EncodeOptions{
	Volume:           256,
	Channels:         2,
	FrameRate:        48000,
	FrameDuration:    20,
	Bitrate:          64,
	Application:      AudioApplicationAudio,
	CompressionLevel: 10,
	PacketLoss:       1,
	BufferedFrames:   100, // At 20ms frames that's 2s
	VBR:              true,
}

// EncodeSession is an encoding session
type EncodeSession interface {
	io.Reader
	OpusReader

	Stop() error                          // Stops the encoding session
	ReadFrame() (frame []byte, err error) // Retrieves a dca frame
	Running() bool                        // Wether its encoding or not
	Options() *EncodeOptions              // Returns the encodeoptions for this session

	// Returns ffmpeg stats, NOTE: this is not playback stats but transcode stats.
	// To get how far into playback you are
	// you have to track the number of frames sent to discord youself
	Stats() *EncodeStats

	// Truncate will throw away all unread frames and kill ffmpeg. call this to make sure there
	// will be no leaks, you don't want ffmpeg processes to start piling up on your system
	Truncate()
}

// EncodeStats is transcode stats reported by ffmpeg
type EncodeStats struct {
	Size     int
	Duration time.Duration
	Bitrate  float32
	Speed    float32
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
	lastStats    *EncodeStats

	lastFrame int

	// buffer that stores unread bytes (not full frames)
	// used to implement io.Reader
	buf bytes.Buffer
}

// EncodedMem encodes data from memory
func EncodeMem(r io.Reader, options *EncodeOptions) (session EncodeSession) {
	s := &encodeSession{
		options:      options,
		pipeReader:   r,
		frameChannel: make(chan []byte, options.BufferedFrames),
	}
	go s.run()
	return s
}

// EncodeFile encodes the file/url/other in path
func EncodeFile(path string, options *EncodeOptions) (session EncodeSession) {
	s := &encodeSession{
		options:      options,
		filePath:     path,
		frameChannel: make(chan []byte, options.BufferedFrames),
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

	vbrStr := "on"
	if !e.options.VBR {
		vbrStr = "off"
	}

	// Launch ffmpeg with a variety of different fruits and goodies mixed togheter
	ffmpeg := exec.Command("ffmpeg", "-stats", "-i", inFile, "-map", "0:a", "-acodec", "libopus", "-f", "ogg", "-vbr", vbrStr,
		"-compression_level", strconv.Itoa(e.options.CompressionLevel), "-vol", strconv.Itoa(e.options.Volume), "-ar", strconv.Itoa(e.options.FrameRate),
		"-ac", strconv.Itoa(e.options.Channels), "-b:a", strconv.Itoa(e.options.Bitrate*1000), "-application", string(e.options.Application),
		"-frame_duration", strconv.Itoa(e.options.FrameDuration), "-packet_loss", strconv.Itoa(e.options.PacketLoss), "pipe:1")

	// logln(ffmpeg.Args)

	if e.pipeReader != nil {
		ffmpeg.Stdin = e.pipeReader
	}

	stdout, err := ffmpeg.StdoutPipe()
	if err != nil {
		e.Unlock()
		logln("StdoutPipe Error:", err)
		close(e.frameChannel)
		return
	}

	stderr, err := ffmpeg.StderrPipe()
	if err != nil {
		e.Unlock()
		logln("StderrPipe Error:", err)
		close(e.frameChannel)
		return
	}

	if !e.options.RawOutput {
		e.writeMetadataFrame()
	}

	// Starts the ffmpeg command
	err = ffmpeg.Start()
	if err != nil {
		e.Unlock()
		logln("RunStart Error:", err)
		close(e.frameChannel)
		return
	}

	e.started = time.Now()

	e.process = ffmpeg.Process
	e.Unlock()

	go e.readStderr(stderr)

	e.readStdout(stdout)
	err = ffmpeg.Wait()
	if err != nil {
		if err.Error() != "signal: killed" {
			logln("Error waiting for ffmpeg:", err)
		}
	}
}

func (e *encodeSession) writeMetadataFrame() {
	// Setup the metadata
	metadata := Metadata{
		Dca: &DCAMetadata{
			Version: FormatVersion,
			Tool: &DCAToolMetadata{
				Name:    "dca",
				Version: LibraryVersion,
				Url:     GitHubRepositoryURL,
				Author:  "jonas747",
			},
		},
		Opus: &OpusMetadata{
			Bitrate:     e.options.Bitrate * 1000,
			SampleRate:  e.options.FrameRate,
			Application: string(e.options.Application),
			FrameSize:   e.options.PCMFrameLen(),
			Channels:    e.options.Channels,
			VBR:         e.options.VBR,
		},
		SongInfo: &SongMetadata{},
		Origin:   &OriginMetadata{},
		Extra:    &ExtraMetadata{},
	}
	var cmdBuf bytes.Buffer
	// get ffprobe data
	if e.pipeReader == nil {
		ffprobe := exec.Command("ffprobe", "-v", "quiet", "-print_format", "json", "-show_format", e.filePath)
		ffprobe.Stdout = &cmdBuf

		err := ffprobe.Start()
		if err != nil {
			logln("RunStart Error:", err)
			return
		}

		err = ffprobe.Wait()
		if err != nil {
			logln("FFprobe Error:", err)
			return
		}
		var ffprobeData *FFprobeMetadata
		err = json.Unmarshal(cmdBuf.Bytes(), &ffprobeData)
		if err != nil {
			logln("Erorr unmarshaling the FFprobe JSON:", err)
			return
		}

		if ffprobeData.Format == nil {
			ffprobeData.Format = &FFprobeFormat{}
		}

		if ffprobeData.Format.Tags == nil {
			ffprobeData.Format.Tags = &FFprobeTags{}
		}

		bitrateInt, err := strconv.Atoi(ffprobeData.Format.Bitrate)
		if err != nil {
			logln("Could not convert bitrate to int:", err)
			return
		}

		metadata.SongInfo = &SongMetadata{
			Title:    ffprobeData.Format.Tags.Title,
			Artist:   ffprobeData.Format.Tags.Artist,
			Album:    ffprobeData.Format.Tags.Album,
			Genre:    ffprobeData.Format.Tags.Genre,
			Comments: e.options.Comment,
		}

		metadata.Origin = &OriginMetadata{
			Source:   "file",
			Bitrate:  bitrateInt,
			Channels: e.options.Channels,
			Encoding: ffprobeData.Format.FormatLongName,
		}

		cmdBuf.Reset()

		// get cover art
		cover := exec.Command("ffmpeg", "-loglevel", "0", "-i", e.filePath, "-f", "singlejpeg", "pipe:1")
		cover.Stdout = &cmdBuf

		err = cover.Start()
		if err != nil {
			logln("RunStart Error:", err)
			return
		}
		var pngBuf bytes.Buffer
		err = cover.Wait()
		if err == nil {
			buf := bytes.NewBufferString(cmdBuf.String())
			var coverImage string
			if e.options.CoverFormat == "png" {
				img, err := jpeg.Decode(buf)
				if err == nil { // silently drop it, no image
					err = png.Encode(&pngBuf, img)
					if err == nil {
						coverImage = base64.StdEncoding.EncodeToString(pngBuf.Bytes())
					}
				}
			} else {
				coverImage = base64.StdEncoding.EncodeToString(cmdBuf.Bytes())
			}

			metadata.SongInfo.Cover = &coverImage
		}

		cmdBuf.Reset()
		pngBuf.Reset()
	} else {
		metadata.Origin = &OriginMetadata{
			Source:   "pipe",
			Channels: e.options.Channels,
			Encoding: "pcm16/s16le",
		}
	}

	// Write the magic header
	jsonData, err := json.Marshal(metadata)
	if err != nil {
		logln("JSon error:", err)
		return
	}
	var buf bytes.Buffer
	buf.Write([]byte(fmt.Sprintf("DCA%d", FormatVersion)))

	// Write the actual json data and length
	jsonLen := int32(len(jsonData))
	err = binary.Write(&buf, binary.LittleEndian, &jsonLen)
	if err != nil {
		logln("Couldn't write json len:", err)
		return
	}

	buf.Write(jsonData)
	e.frameChannel <- buf.Bytes()
}

func (e *encodeSession) readStderr(stderr io.ReadCloser) {
	bufReader := bufio.NewReader(stderr)
	outBuf := ""
	for {
		r, _, err := bufReader.ReadRune()
		if err != nil {
			if err != io.EOF {
				logln("Error Reading stderr:", err)
			}
			break
		}

		if r == '\n' || r == '\r' {
			if len(outBuf) > 0 {
				e.handleStderrLine(outBuf)
				outBuf = ""
			}
		} else {
			outBuf += string(r)
		}
	}
}

func (e *encodeSession) handleStderrLine(line string) {
	if strings.Index(line, "size=") != 0 {
		return // Not stats info
	}

	var size int

	var timeH int
	var timeM int
	var timeS float32

	var bitrate float32
	var speed float32

	_, err := fmt.Sscanf(line, "size=%dkB time=%d:%d:%f bitrate=%fkbits/s speed=%fx", &size, &timeH, &timeM, &timeS, &bitrate, &speed)
	if err != nil {
		logln("Error parsing ffmpeg stats:", err)
	}

	dur := time.Duration(timeH) * time.Hour
	dur += time.Duration(timeM) * time.Minute
	dur += time.Duration(timeS) * time.Second

	stats := &EncodeStats{
		Size:     size,
		Duration: dur,
		Bitrate:  bitrate,
		Speed:    speed,
	}

	e.Lock()
	e.lastStats = stats
	e.Unlock()
}

func (e *encodeSession) readStdout(stdout io.ReadCloser) {
	defer close(e.frameChannel)

	decoder := ogg.NewDecoder(stdout)

	var packetBuf bytes.Buffer

	for {
		// Retrieve a page
		page, err := decoder.Decode()
		if err != nil {
			if err != io.EOF {
				logln("Error reading fmmpeg stdout:", err)
			}
			break
		}

		// The current position in the page data
		curPos := 0

		readSegs := 0
		// Read all the opus frames from the segment table
		for _, seg := range page.SegTbl {
			packetBuf.Write(page.Packet[curPos : curPos+int(seg)])
			curPos += int(seg)
			readSegs++

			// Min size of an opus packet is 1 byte, can't be smaller
			if seg < 255 && packetBuf.Len() > 0 {

				// segment length is less than 255, end of packet
				err = e.writeOpusFrame(packetBuf.Bytes())
				if err != nil {
					logln("Error writing opus frame:", err)
					break
				}
				packetBuf.Reset()
			}
		}
	}
	if packetBuf.Len() > 0 {
		err := e.writeOpusFrame(packetBuf.Bytes())
		if err != nil {
			logln("Error writing opus frame:", err)
		}
	}
}

func (e *encodeSession) writeOpusFrame(opusFrame []byte) error {
	var dcaBuf bytes.Buffer

	err := binary.Write(&dcaBuf, binary.LittleEndian, int16(len(opusFrame)))
	if err != nil {
		return err
	}

	_, err = dcaBuf.Write(opusFrame)
	if err != nil {
		return err
	}

	e.frameChannel <- dcaBuf.Bytes()

	e.Lock()
	e.lastFrame++
	e.Unlock()

	return nil
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

// ReadFrame blocks untill a frame is read or there are no more frames
// Note: If rawoutput is not set, the first frame will be a metadata frame
func (e *encodeSession) ReadFrame() (frame []byte, err error) {
	frame = <-e.frameChannel
	if frame == nil {
		return nil, io.EOF
	}

	return frame, nil
}

func (e *encodeSession) OpusFrame() (frame []byte, err error) {
	return DecodeFrame(e)
}

// Running return true if running
func (e *encodeSession) Running() (running bool) {
	e.Lock()
	running = e.running
	e.Unlock()
	return
}

func (e *encodeSession) Stats() *EncodeStats {
	s := &EncodeStats{}
	e.Lock()
	if e.lastStats != nil {
		*s = *e.lastStats
	}
	e.Unlock()

	return s
}

func (e *encodeSession) Options() *EncodeOptions {
	return e.options
}

func (e *encodeSession) Truncate() {
	e.Stop()

	for _ = range e.frameChannel {
		// empty till closed
		// Cats can be right-pawed or left-pawed.
	}
}

// implement io.Reader
func (e *encodeSession) Read(p []byte) (n int, err error) {
	if e.buf.Len() >= len(p) {
		return e.buf.Read(p)
	}

	for e.buf.Len() < len(p) {
		f, err := e.ReadFrame()
		if err != nil {
			break
		}
		e.buf.Write(f)
	}

	return e.buf.Read(p)
}

func (e *encodeSession) FrameDuration() int {
	return e.options.FrameDuration
}
