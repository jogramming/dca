package main

import (
	"bytes"
	"flag"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/jonas747/dca"
	"io/ioutil"
	"log"
	"os/exec"
	"runtime"
)

var (
	run *exec.Cmd
)

func main() {

	// NOTE: All of the below fields are required for this example to work correctly.
	var (
		Token     = flag.String("t", "", "Discord token.")
		GuildID   = flag.String("g", "", "Guild ID")
		ChannelID = flag.String("c", "", "Channel ID")
		Folder    = flag.String("f", "sounds", "Folder of files to play.")
		err       error
	)
	flag.Parse()

	if *GuildID == "" {
		log.Fatal("No guild specified")
	}

	if *ChannelID == "" {
		log.Println("No channdlid specified, using guildid (default voice channel if not deleted)")
		ChannelID = GuildID
	}

	if *Token == "" {
		log.Fatal("No token specified!")
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Connect to Discord
	discord, err := discordgo.New(*Token)
	if err != nil {
		log.Fatal(err)
	}
	discord.LogLevel = discordgo.LogInformational

	// Open Websocket
	err = discord.Open()
	if err != nil {
		log.Fatal(err)
	}

	// Connect to voice channel.
	// NOTE: Setting mute to false, deaf to true.
	voice, err := discord.ChannelVoiceJoin(*GuildID, *ChannelID, false, true)
	if err != nil {
		log.Fatal(err)
	}
	voice.LogLevel = discordgo.LogInformational

	// Hacky loop to prevent sending on a nil channel.
	// TODO: Find a better way.
	for voice.Ready == false {
		runtime.Gosched()
	}

	// Start loop and attempt to play all files in the given folder
	fmt.Println("Reading Folder: ", *Folder)
	files, _ := ioutil.ReadDir(*Folder)
	for _, f := range files {
		fmt.Println("PlayAudioFile:", f.Name())
		discord.UpdateStatus(0, f.Name())
		PlayAudioFile(voice, fmt.Sprintf("%s/%s", *Folder, f.Name()))
	}
	// Close connections
	voice.Close()
	discord.Close()

	return
}

// PlayAudioFile will play the given filename to the already connected
// Discord voice server/channel.  voice websocket and udp socket
// must already be setup before this will work.
func PlayAudioFile(v *discordgo.VoiceConnection, filename string) {
	opts := dca.StdEncodeOptions
	opts.RawOutput = true
	opts.Bitrate = 128

	encodeSession := dca.EncodeFile(filename, opts)

	// Send "speaking" packet over the voice websocket
	err := v.Speaking(true)
	if err != nil {
		log.Fatal("Failed setting speaking", err)
	}

	// Send not "speaking" packet over the websocket when we finish
	defer v.Speaking(false)

	for {
		frame, err := encodeSession.ReadFrame()
		if err != nil {
			break
		}

		audio, err := dca.DecodeFrame(bytes.NewBuffer(frame))
		if err != nil {
			continue // Make sure we read all he frames, otherwise theres a leak!
		}

		v.OpusSend <- audio
	}
}
