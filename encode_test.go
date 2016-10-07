package dca

import (
	"testing"
)

func TestEncode(t *testing.T) {
	session := EncodeFile("testaudio.ogg", StdEncodeOptions)

	numFrames := 0
	for {
		_, err := session.ReadFrame()
		if err != nil {
			break
		}
		numFrames++
	}

	// Predermined, probably gonna change the testing method somehow
	if numFrames != 758 {
		t.Errorf("Incorrect number of frames (got %d expected %d)", numFrames, 756)
		t.Fail()
	}
}
