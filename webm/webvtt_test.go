// Copyright 2019 The ebml-go authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package webm

import (
	"bytes"
	"io"
	"reflect"
	"testing"

	"github.com/at-wat/ebml-go"
	"github.com/at-wat/ebml-go/internal/buffercloser"
	"github.com/at-wat/ebml-go/mkvcore"
)

func TestWebVTT(t *testing.T) {
	tracks := []TrackEntry{
		{
			Name:        "Video",
			TrackNumber: 1,
			TrackUID:    1,
			CodecID:     "V_VP8",
			TrackType:   1,
			Video: &Video{
				PixelWidth:  320,
				PixelHeight: 240,
			},
		},
		{
			Name:        "Audio",
			TrackNumber: 2,
			TrackUID:    2,
			CodecID:     "A_OPUS",
			TrackType:   2,
			Audio: &Audio{
				SamplingFrequency: 48000.0,
				Channels:          2,
			},
		},
		WebVTTTrackEntry(3, 3, "Subtitle"),
	}

	t.Run("DefaultEntry", func(t *testing.T) {
		buf := buffercloser.New()

		ws, err := NewSimpleBlockWriter(buf, tracks)
		if err != nil {
			t.Fatalf("Failed to create BlockWriter: '%v'", err)
		}
		if len(ws) != len(tracks) {
			t.Fatalf("Expected %d writers, got %d", len(tracks), len(ws))
		}

		// Audio and video are interleaved with the subtitle events.
		// Subtitle events are submitted as soon as they are known, which can
		// be behind the already written audio/video timeline.
		for i := int64(0); i < 30; i++ {
			if _, err := ws[0].Write(i%10 == 0, i*20, []byte{0x01}); err != nil {
				t.Fatalf("Failed to Write: '%v'", err)
			}
			if _, err := ws[1].Write(true, i*20, []byte{0x02}); err != nil {
				t.Fatalf("Failed to Write: '%v'", err)
			}
		}

		events := []WebVTTEvent{
			{Start: 50, Duration: 800, Text: "one"},
			{Start: 300, Duration: 1200, Text: "two"},
			{Start: 2000, Duration: 400, Text: "three"},
		}
		subW, ok := ws[2].(mkvcore.BlockGroupWriter)
		if !ok {
			t.Fatal("WebM subtitle writer must implement mkvcore.BlockGroupWriter")
		}
		for _, e := range events {
			if n, err := WriteWebVTTEvent(subW, e); err != nil {
				t.Fatalf("Failed to WriteWebVTTEvent: '%v'", err)
			} else if n != len(e.Text) {
				t.Errorf("Expected return value %d, got %d", len(e.Text), n)
			}
		}

		for _, w := range ws {
			w.Close()
		}
		<-buf.Closed()

		// Read back events.
		rs, err := mkvcore.NewSimpleBlockReader(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("Failed to create BlockReader: '%v'", err)
		}
		if len(rs) != len(tracks) {
			t.Fatalf("Expected %d readers, got %d", len(tracks), len(rs))
		}

		subR, ok := rs[2].(mkvcore.BlockGroupReader)
		if !ok {
			t.Fatal("WebM subtitle reader must implement mkvcore.BlockGroupReader")
		}

		// Drain the audio/video tracks concurrently.
		done := make(chan struct{}, 2)
		for _, r := range rs[:2] {
			go func(r mkvcore.BlockReader) {
				for {
					if _, _, _, err := r.Read(); err != nil {
						break
					}
				}
				done <- struct{}{}
			}(r)
		}

		var got []WebVTTEvent
		for {
			e, err := ReadWebVTTEvent(subR)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("Failed to ReadWebVTTEvent: '%v'", err)
			}
			got = append(got, e)
		}

		expected := []WebVTTEvent{
			{Start: 50, Duration: 800, Text: "one", Keyframe: true},
			{Start: 300, Duration: 1200, Text: "two", Keyframe: true},
			{Start: 2000, Duration: 400, Text: "three", Keyframe: true},
		}
		if !reflect.DeepEqual(expected, got) {
			t.Errorf("Expected events: %v, got: %v", expected, got)
		}

		<-done
		<-done

		// The file must be a valid WebM with BlockGroups in the subtitle
		// track and SimpleBlocks in the audio/video tracks.
		var result struct {
			Header  EBMLHeader `ebml:"EBML"`
			Segment Segment    `ebml:"Segment,size=unknown"`
		}
		if err := ebml.Unmarshal(bytes.NewReader(buf.Bytes()), &result); err != nil {
			t.Fatalf("Failed to Unmarshal resultant binary: '%v'", err)
		}
		if !reflect.DeepEqual(tracks, result.Segment.Tracks.TrackEntry) {
			t.Errorf("Unexpected tracks: %v", result.Segment.Tracks.TrackEntry)
		}

		var nSimple, nGroup int
		for _, c := range result.Segment.Cluster {
			nSimple += len(c.SimpleBlock)
			nGroup += len(c.BlockGroup)
			for _, g := range c.BlockGroup {
				if g.Block.TrackNumber != 3 {
					t.Errorf("Expected BlockGroup only on subtitle track, got track %d", g.Block.TrackNumber)
				}
			}
			for _, b := range c.SimpleBlock {
				if b.TrackNumber == 3 {
					t.Error("Subtitle event must not be written as SimpleBlock")
				}
			}
		}
		if nSimple != 60 {
			t.Errorf("Expected 60 SimpleBlocks, got %d", nSimple)
		}
		if nGroup != len(events) {
			t.Errorf("Expected %d BlockGroups, got %d", len(events), nGroup)
		}
	})

	t.Run("SimpleBlockOnly", func(t *testing.T) {
		// A file with only SimpleBlocks must be read as before.
		buf := buffercloser.New()
		ws, err := NewSimpleBlockWriter(buf, tracks[:2])
		if err != nil {
			t.Fatalf("Failed to create BlockWriter: '%v'", err)
		}
		if _, err := ws[0].Write(true, 100, []byte{0x01}); err != nil {
			t.Fatalf("Failed to Write: '%v'", err)
		}
		if _, err := ws[1].Write(true, 110, []byte{0x02}); err != nil {
			t.Fatalf("Failed to Write: '%v'", err)
		}
		for _, w := range ws {
			w.Close()
		}
		<-buf.Closed()

		rs, err := mkvcore.NewSimpleBlockReader(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("Failed to create BlockReader: '%v'", err)
		}
		// Audio track must be drained concurrently with the video track.
		go func() {
			for {
				if _, _, _, err := rs[1].Read(); err != nil {
					return
				}
			}
		}()

		b, keyframe, timestamp, err := rs[0].Read()
		if err != nil {
			t.Fatalf("Failed to Read: '%v'", err)
		}
		if !bytes.Equal(b, []byte{0x01}) || !keyframe || timestamp != 0 {
			t.Errorf("Unexpected block: b=%v keyframe=%v timestamp=%v", b, keyframe, timestamp)
		}
		if _, _, _, duration, err := rs[0].(mkvcore.BlockGroupReader).ReadBlockGroup(); err != io.EOF {
			// First Read consumed the block; ReadBlockGroup must return the
			// next one, which doesn't exist.
			t.Errorf("Expected EOF, got '%v' with duration %d", err, duration)
		}
	})
}
