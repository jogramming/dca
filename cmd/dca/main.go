package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/jonas747/dca"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// Define constants
const (
	// The current version of the DCA format
	FormatVersion int8 = 1

	// The current version of the DCA program
	ProgramVersion string = "0.0.1"

	// The URL to the GitHub repository of DCA
	GitHubRepositoryURL string = "https://github.com/bwmarrin/dca"
)

// All global variables used within the program
var (
	// Buffer for some commands
	CmdBuf bytes.Buffer
	PngBuf bytes.Buffer

	CoverImage string

	// Metadata structures
	Metadata    dca.MetadataStruct
	FFprobeData dca.FFprobeMetadata

	// Magic bytes to write at the start of a DCA file
	MagicBytes string = fmt.Sprintf("DCA%d", FormatVersion)

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

	wg sync.WaitGroup
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

	if RawOutput == false {
		// Setup the metadata
		Metadata = dca.MetadataStruct{
			Dca: &dca.DCAMetadata{
				Version: FormatVersion,
				Tool: &dca.DCAToolMetadata{
					Name:    "dca",
					Version: ProgramVersion,
					Url:     GitHubRepositoryURL,
					Author:  "bwmarrin",
				},
			},
			SongInfo: &dca.SongMetadata{},
			Origin:   &dca.OriginMetadata{},
			Opus: &dca.OpusMetadata{
				Bitrate:     Bitrate * 1000,
				SampleRate:  FrameRate,
				Application: Application,
				FrameSize:   FrameDuration * 48,
				Channels:    Channels,
			},
			Extra: &dca.ExtraMetadata{},
		}
		_ = Metadata

		// get ffprobe data
		if InFile != "pipe:0" {
			ffprobe := exec.Command("ffprobe", "-v", "quiet", "-print_format", "json", "-show_format", InFile)
			ffprobe.Stdout = &CmdBuf

			err = ffprobe.Start()
			if err != nil {
				fmt.Println("RunStart Error:", err)
				return
			}

			err = ffprobe.Wait()
			if err != nil {
				fmt.Println("FFprobe Error:", err)
				return
			}
			err = json.Unmarshal(CmdBuf.Bytes(), &FFprobeData)
			if err != nil {
				fmt.Println("Erorr unmarshaling the FFprobe JSON:", err)
				return
			}

			bitrateInt, err := strconv.Atoi(FFprobeData.Format.Bitrate)
			if err != nil {
				fmt.Println("Could not convert bitrate to int:", err)
				return
			}

			if FFprobeData.Format.Tags == nil {
				FFprobeData.Format.Tags = &dca.FFprobeTags{}
			}

			Metadata.SongInfo = &dca.SongMetadata{
				Title:    FFprobeData.Format.Tags.Title,
				Artist:   FFprobeData.Format.Tags.Artist,
				Album:    FFprobeData.Format.Tags.Album,
				Genre:    FFprobeData.Format.Tags.Genre,
				Comments: "", // change later?
			}

			Metadata.Origin = &dca.OriginMetadata{
				Source:   "file",
				Bitrate:  bitrateInt,
				Channels: Channels,
				Encoding: FFprobeData.Format.FormatLongName,
			}

			CmdBuf.Reset()

			// get cover art
			cover := exec.Command("ffmpeg", "-loglevel", "0", "-i", InFile, "-f", "singlejpeg", "pipe:1")
			cover.Stdout = &CmdBuf

			err = cover.Start()
			if err != nil {
				fmt.Println("RunStart Error:", err)
				return
			}

			err = cover.Wait()
			if err == nil {
				buf := bytes.NewBufferString(CmdBuf.String())

				if CoverFormat == "png" {
					img, err := jpeg.Decode(buf)
					if err == nil { // silently drop it, no image
						err = png.Encode(&PngBuf, img)
						if err == nil {
							CoverImage = base64.StdEncoding.EncodeToString(PngBuf.Bytes())
						}
					}
				} else {
					CoverImage = base64.StdEncoding.EncodeToString(CmdBuf.Bytes())
				}

				Metadata.SongInfo.Cover = &CoverImage
			}

			CmdBuf.Reset()
			PngBuf.Reset()
		} else {
			Metadata.Origin = &dca.OriginMetadata{
				Source:   "pipe",
				Channels: Channels,
				Encoding: "pcm16/s16le",
			}
		}
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
