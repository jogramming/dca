package dca

import (
	"github.com/bwmarrin/discordgo"
	"io"
	"sync"
	"time"
)

// StreamingSession provides an easy way to directly transmit opus audio
// to discord from an encode session, use EncodeSession.StreamToDiscord
type StreamingSession struct {
	sync.Mutex

	e          EncodeSession
	vc         *discordgo.VoiceConnection
	paused     bool
	framesSent int

	finished bool
	running  bool
	err      error // If an error occured and we had to stop
}

func StreamFromEncodeSession(e EncodeSession, vc *discordgo.VoiceConnection) *StreamingSession {
	session := &StreamingSession{
		e:  e,
		vc: vc,
	}

	go session.stream()

	return session
}

func (s *StreamingSession) stream() {
	// Check if we are already running and if so stop
	s.Lock()
	if s.running {
		s.Unlock()
		return
	}
	s.running = true
	s.Unlock()

	defer func() {
		s.Lock()
		s.running = false
		s.Unlock()
	}()

	for {
		s.Lock()
		if s.paused {
			s.Unlock()
			return
		}
		s.Unlock()

		err := s.readNext()
		if err != nil {
			s.Lock()

			s.finished = true
			if err != io.EOF {
				s.err = err
			}

			// Make sure there are no leaks
			s.e.Truncate()

			s.Unlock()
			break
		}
	}
}

func (s *StreamingSession) readNext() error {
	opus, err := DecodeFrame(s.e)
	if err != nil {
		return err
	}

	s.vc.OpusSend <- opus

	s.Lock()
	s.framesSent++
	s.Unlock()

	return nil
}

// SetRunning provides pause/unpause functionality
func (s *StreamingSession) SetRunning(running bool) {
	s.Lock()

	if s.finished {
		s.Unlock()
		return
	}

	// Already running
	if running && s.running {
		if s.paused {
			// Was set to stop running after next frame so undo this
			s.paused = false
		}

		s.Unlock()
		return
	}

	// Already stopped
	if !running && !s.running {
		// Not running, but starting up..
		if !s.paused {
			s.paused = true
		}

		s.Unlock()
		return
	}

	// Time to start it up again
	if !s.running && s.paused && running {
		go s.stream()
	}

	s.paused = !running
	s.Unlock()
}

// PlaybackPosition returns the the duration of content we have transmitted so far
func (s *StreamingSession) PlaybackPosition() time.Duration {
	s.Lock()
	dur := time.Duration(s.framesSent*s.e.Options().FrameDuration) * time.Millisecond
	s.Unlock()
	return dur
}

// Finished returns wether the stream finished or not, and any error that caused it to stop
func (s *StreamingSession) Finished() (bool, error) {
	s.Lock()
	err := s.err
	fin := s.finished
	s.Unlock()

	return fin, err
}

func (s *StreamingSession) Paused() bool {
	s.Lock()
	p := s.paused
	s.Unlock()

	return p
}
