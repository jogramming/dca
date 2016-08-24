package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os/exec"
	"runtime"

	"github.com/bwmarrin/discordgo"
	"github.com/jonas747/dca"
)

var (
	run *exec.Cmd
)

func main() {

	// NOTE: All of the below fields are required for this example to work correctly.
	var (
		Token     = flag.String("t", "", "Discord token.")
		GuildID   = flag.String("g", "183362174188650497", "Guild ID")
		ChannelID = flag.String("c", "183362174188650498", "Channel ID")
		Folder    = flag.String("f", "sounds", "Folder of files to play.")
		err       error
	)
	flag.Parse()

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

	encodeSession := dca.EncodeFile(filename, nil)

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
			break
		}

		v.OpusSend <- audio
	}

	// Empty the shizzazz
	if encodeSession.Running() {
		encodeSession.Stop()
		for {
			_, err = encodeSession.ReadFrame()
			if err != nil {
				break
			}
		}
	}
}
