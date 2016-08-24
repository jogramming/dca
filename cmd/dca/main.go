package main

import (
	"flag"
	"fmt"
	"github.com/jonas747/dca"
	"os"
)

// All global variables used within the program
var (
	// Magic bytes to write at the start of a DCA file

	// 1 for mono, 2 for stereo
	Channels int

	// Must be one of 8000, 12000, 16000, 24000, or 48000.
	// Discord only uses 48000 currently.
	FrameRate int

	// Rates from 500 to 512000 bits per second are meaningful
	// Discord only uses 8000 to 128000 and default is 64000
	Bitrate int

	// Must be one of voip, audio, or lowdelay.
	// DCA defaults to audio which is ideal for music
	// Not sure what Discord uses here, probably voip
	Application string

	// if true, dca sends raw output without any magic bytes or json metadata
	RawOutput bool

	FrameDuration int // uint16 size of each audio frame

	Volume int // change audio volume (256=normal)

	//OpusEncoder *gopus.Encoder

	InFile      string
	CoverFormat string = "jpeg"

	OutFile string = "pipe:1"

	err error
)

// init configures and parses the command line arguments
func init() {

	flag.StringVar(&InFile, "i", "pipe:0", "infile")
	flag.IntVar(&Volume, "vol", 256, "change audio volume (256=normal)")
	flag.IntVar(&Channels, "ac", 2, "audio channels")
	flag.IntVar(&FrameRate, "ar", 48000, "audio sampling rate")
	flag.IntVar(&FrameDuration, "as", 20, "audio frame duration can be 20, 40, or 60 (ms)")
	flag.IntVar(&Bitrate, "ab", 128, "audio encoding bitrate in kb/s can be 8 - 128")
	flag.BoolVar(&RawOutput, "raw", false, "Raw opus output (no metadata or magic bytes)")
	flag.StringVar(&Application, "aa", "audio", "audio application can be voip, audio, or lowdelay")
	flag.StringVar(&CoverFormat, "cf", "jpeg", "format the cover art will be encoded with")

	if len(os.Args) < 2 {
		flag.Usage()
		os.Exit(1)
	}

	flag.Parse()
}

// very simple program that wraps ffmpeg and outputs raw opus data frames
// with a uint16 header for each frame with the frame length in bytes
func main() {

	//////////////////////////////////////////////////////////////////////////
	// BLOCK : Basic setup and validation
	//////////////////////////////////////////////////////////////////////////

	// If only one argument provided assume it's a filename.
	if len(os.Args) == 2 {
		InFile = os.Args[1]
	}

	// If reading from a file, verify it exists.
	if InFile != "pipe:0" {

		if _, err := os.Stat(InFile); os.IsNotExist(err) {
			fmt.Println("error: infile does not exist")
			flag.Usage()
			return
		}
	}

	// If reading from pipe, make sure pipe is open
	if InFile == "pipe:0" {
		fi, err := os.Stdin.Stat()
		if err != nil {
			fmt.Println(err)
			return
		}

		if (fi.Mode() & os.ModeCharDevice) == 0 {
		} else {
			fmt.Println("error: stdin is not a pipe.")
			flag.Usage()
			return
		}
	}

	//////////////////////////////////////////////////////////////////////////
	// BLOCK : Create chans, buffers, and encoder for use
	//////////////////////////////////////////////////////////////////////////

	if Bitrate < 1 || Bitrate > 512 {
		Bitrate = 64 // Set to Discord default
	}

	//////////////////////////////////////////////////////////////////////////
	// BLOCK : Start reader and writer workers
	//////////////////////////////////////////////////////////////////////////

	var session dca.EncodeSession
	options := &dca.EncodeOptions{
		Volume:        Volume,
		Channels:      Channels,
		FrameRate:     FrameRate,
		FrameDuration: FrameDuration,
		Bitrate:       Bitrate,
		RawOutput:     RawOutput,
		Application:   dca.AudioApplication(Application),
		CoverFormat:   CoverFormat,
	}

	var output = os.Stdout

	if InFile == "pipe:0" {
		session = dca.EncodeMem(os.Stdin, options)
	} else {
		session = dca.EncodeFile(InFile, options)
	}

	for {
		frame, err := session.ReadFrame()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error reading frame", err)
			break
		}
		_, err = output.Write(frame)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error writing frame", err)
			break
		}
	}
}
