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

package mkvcore

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/at-wat/ebml-go"
	"github.com/at-wat/ebml-go/internal/buffercloser"
)

func TestBlockWriter_BlockGroup(t *testing.T) {
	t.Run("RoundTrip", func(t *testing.T) {
		buf := buffercloser.New()

		ws, err := NewSimpleBlockWriter(
			buf,
			[]TrackDescription{
				{TrackNumber: 1, TrackEntry: map[string]interface{}{"TrackNumber": uint(1)}},
				{TrackNumber: 2, TrackEntry: map[string]interface{}{"TrackNumber": uint(2)}},
			},
			WithEBMLHeader(nil),
			WithSegmentInfo(nil),
			WithSeekHead(false),
		)
		if err != nil {
			t.Fatalf("Failed to create BlockWriter: '%v'", err)
		}

		gow, ok := ws[1].(BlockGroupWriter)
		if !ok {
			t.Fatal("BlockWriteCloser must implement BlockGroupWriter")
		}

		if _, err := ws[0].Write(true, 0, []byte{0x01}); err != nil {
			t.Fatalf("Failed to Write: '%v'", err)
		}
		if _, err := gow.WriteBlockGroup(true, 100, 1500, []byte("hello")); err != nil {
			t.Fatalf("Failed to WriteBlockGroup: '%v'", err)
		}
		if _, err := gow.WriteBlockGroup(false, 2000, 500, []byte("world")); err != nil {
			t.Fatalf("Failed to WriteBlockGroup: '%v'", err)
		}

		for _, w := range ws {
			if err := w.Close(); err != nil {
				t.Fatalf("Failed to Close: '%v'", err)
			}
		}
		<-buf.Closed()

		// The subtitle track must be stored as BlockGroups with BlockDuration.
		var result struct {
			Segment cuesTestSegment `ebml:"Segment,size=unknown"`
		}
		if err := ebml.Unmarshal(bytes.NewReader(buf.Bytes()), &result); err != nil {
			t.Fatalf("Failed to Unmarshal: '%v'", err)
		}

		var groups []simpleBlockGroup
		var simpleBlocks []ebml.Block
		for _, c := range result.Segment.Cluster {
			groups = append(groups, c.BlockGroup...)
			simpleBlocks = append(simpleBlocks, c.SimpleBlock...)
		}
		if len(groups) != 2 {
			t.Fatalf("Expected 2 BlockGroups, got %d", len(groups))
		}
		if len(simpleBlocks) != 1 {
			t.Fatalf("Expected 1 SimpleBlock, got %d", len(simpleBlocks))
		}
		if groups[0].BlockDuration != 1500 {
			t.Errorf("Expected BlockDuration 1500, got %d", groups[0].BlockDuration)
		}
		if groups[1].BlockDuration != 500 {
			t.Errorf("Expected BlockDuration 500, got %d", groups[1].BlockDuration)
		}
		if len(groups[0].ReferenceBlock) != 0 {
			t.Errorf("Keyframe BlockGroup must not have ReferenceBlock, got %v", groups[0].ReferenceBlock)
		}
		if len(groups[1].ReferenceBlock) == 0 {
			t.Error("Non-keyframe BlockGroup must have ReferenceBlock")
		}

		// Read back with the block reader.
		rs, err := NewSimpleBlockReader(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("Failed to create BlockReader: '%v'", err)
		}
		if len(rs) != 2 {
			t.Fatalf("Expected 2 readers, got %d", len(rs))
		}

		gr, ok := rs[1].(BlockGroupReader)
		if !ok {
			t.Fatal("BlockReadCloser must implement BlockGroupReader")
		}

		// Tracks must be drained concurrently; the reader delivers blocks in
		// file order.
		type event struct {
			text     string
			start    int64
			duration int64
			keyframe bool
		}
		chEvents := make(chan []event, 1)
		chSimple := make(chan struct {
			b        []byte
			keyframe bool
			tc       int64
			err      error
		}, 1)
		go func() {
			var events []event
			for {
				b, keyframe, timestamp, duration, err := gr.ReadBlockGroup()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Errorf("Failed to ReadBlockGroup: '%v'", err)
					break
				}
				events = append(events, event{string(b), timestamp, duration, keyframe})
			}
			chEvents <- events
		}()
		go func() {
			b, keyframe, timestamp, err := rs[0].Read()
			chSimple <- struct {
				b        []byte
				keyframe bool
				tc       int64
				err      error
			}{b, keyframe, timestamp, err}
		}()

		got := <-chEvents
		expected := []event{
			{"hello", 100, 1500, true},
			{"world", 2000, 500, false},
		}
		if !reflect.DeepEqual(expected, got) {
			t.Errorf("Expected events: %v, got: %v", expected, got)
		}

		// The simple block reader must still work as before.
		s := <-chSimple
		if s.err != nil {
			t.Fatalf("Failed to Read: '%v'", s.err)
		}
		if !bytes.Equal(s.b, []byte{0x01}) || !s.keyframe || s.tc != 0 {
			t.Errorf("Unexpected SimpleBlock: b=%v keyframe=%v timestamp=%v", s.b, s.keyframe, s.tc)
		}
	})

	t.Run("ZeroDuration", func(t *testing.T) {
		buf := buffercloser.New()
		ws, err := NewSimpleBlockWriter(
			buf, []TrackDescription{{TrackNumber: 1, TrackEntry: map[string]interface{}{"TrackNumber": uint(1)}}},
			WithEBMLHeader(nil), WithSegmentInfo(nil),
		)
		if err != nil {
			t.Fatalf("Failed to create BlockWriter: '%v'", err)
		}
		gow := ws[0].(BlockGroupWriter)
		if _, err := gow.WriteBlockGroup(true, 10, 0, []byte("x")); err != nil {
			t.Fatalf("Failed to WriteBlockGroup: '%v'", err)
		}
		ws[0].Close()
		<-buf.Closed()

		rs, err := NewSimpleBlockReader(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("Failed to create BlockReader: '%v'", err)
		}
		b, keyframe, timestamp, duration, err := rs[0].(BlockGroupReader).ReadBlockGroup()
		if err != nil {
			t.Fatalf("Failed to ReadBlockGroup: '%v'", err)
		}
		// The first written block defines the timeline origin.
		if string(b) != "x" || !keyframe || timestamp != 0 || duration != 0 {
			t.Errorf("Unexpected BlockGroup: b=%q keyframe=%v timestamp=%v duration=%v",
				b, keyframe, timestamp, duration)
		}
	})

	t.Run("NegativeDuration", func(t *testing.T) {
		buf := buffercloser.New()
		ws, err := NewSimpleBlockWriter(
			buf, []TrackDescription{{TrackNumber: 1, TrackEntry: map[string]interface{}{"TrackNumber": uint(1)}}},
			WithEBMLHeader(nil), WithSegmentInfo(nil),
		)
		if err != nil {
			t.Fatalf("Failed to create BlockWriter: '%v'", err)
		}
		_, err = ws[0].(BlockGroupWriter).WriteBlockGroup(true, 10, -1, []byte("x"))
		if !errors.Is(err, ErrInvalidBlockDuration) {
			t.Errorf("Expected error '%v', got '%v'", ErrInvalidBlockDuration, err)
		}
		ws[0].Close()
		<-buf.Closed()
	})

	t.Run("LateEvent", func(t *testing.T) {
		buf := buffercloser.New()
		ws, err := NewSimpleBlockWriter(
			buf,
			[]TrackDescription{
				{TrackNumber: 1, TrackEntry: map[string]interface{}{"TrackNumber": uint(1)}},
				{TrackNumber: 2, TrackEntry: map[string]interface{}{"TrackNumber": uint(2)}},
			},
			WithEBMLHeader(nil), WithSegmentInfo(nil),
		)
		if err != nil {
			t.Fatalf("Failed to create BlockWriter: '%v'", err)
		}

		// Dense track advances far ahead of the sparse subtitle track.
		for i := int64(0); i < 100; i++ {
			if _, err := ws[0].Write(true, i*20, []byte{0x01}); err != nil {
				t.Fatalf("Failed to Write: '%v'", err)
			}
		}
		gow := ws[1].(BlockGroupWriter)
		for _, e := range []struct {
			start, duration int64
			text            string
		}{
			{100, 300, "a"},
			{450, 200, "b"},
			{1500, 100, "c"},
		} {
			if _, err := gow.WriteBlockGroup(true, e.start, e.duration, []byte(e.text)); err != nil {
				t.Fatalf("Failed to WriteBlockGroup: '%v'", err)
			}
		}
		for _, w := range ws {
			w.Close()
		}
		<-buf.Closed()

		rs, err := NewSimpleBlockReader(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("Failed to create BlockReader: '%v'", err)
		}
		gr := rs[1].(BlockGroupReader)

		// Both tracks must be drained concurrently.
		chEvents := make(chan []blockGroupEvent, 1)
		type simpleResult struct {
			n   int
			err error
		}
		chSimple := make(chan simpleResult, 1)
		go func() {
			var got []blockGroupEvent
			for {
				b, keyframe, timestamp, duration, err := gr.ReadBlockGroup()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Errorf("Failed to ReadBlockGroup: '%v'", err)
					break
				}
				got = append(got, blockGroupEvent{string(b), timestamp, duration, keyframe})
			}
			chEvents <- got
		}()
		go func() {
			n := 0
			for {
				if _, _, _, err := rs[0].Read(); err == io.EOF {
					break
				} else if err != nil {
					chSimple <- simpleResult{n, err}
					return
				}
				n++
			}
			chSimple <- simpleResult{n, nil}
		}()

		got := <-chEvents
		expected := []blockGroupEvent{
			{"a", 100, 300, true},
			{"b", 450, 200, true},
			{"c", 1500, 100, true},
		}
		if !reflect.DeepEqual(expected, got) {
			t.Errorf("Expected events: %v, got: %v", expected, got)
		}

		// The dense track must be intact.
		s := <-chSimple
		if s.err != nil {
			t.Fatalf("Failed to Read: '%v'", s.err)
		}
		if s.n != 100 {
			t.Errorf("Expected 100 frames, got %d", s.n)
		}
	})
}

// blockGroupEvent is a helper to compare read events in tests.
type blockGroupEvent struct {
	Text     string
	Start    int64
	Duration int64
	Keyframe bool
}

func TestBlockWriter_BlockGroupVeryLateEvent(t *testing.T) {
	buf := newSeekableBuffer()
	ws, err := NewSimpleBlockWriter(
		buf,
		[]TrackDescription{
			{TrackNumber: 1, TrackEntry: map[string]interface{}{"TrackNumber": uint(1)}},
			{TrackNumber: 2, TrackEntry: map[string]interface{}{"TrackNumber": uint(2)}},
		},
		WithEBMLHeader(nil),
		WithSegmentInfo(&durationInfo{TimecodeScale: 1000000}),
		WithSeekHead(true),
		WithCues(4096),
	)
	if err != nil {
		t.Fatalf("Failed to create BlockWriter: '%v'", err)
	}

	// Advance the dense track far beyond the maximum cluster-relative timecode.
	for i := int64(0); i < 100; i++ {
		if _, err := ws[0].Write(true, i*1000, []byte{0x01}); err != nil {
			t.Fatalf("Failed to Write: '%v'", err)
		}
	}
	// A subtitle event whose start time is already far in the past of the
	// current cluster.
	gow := ws[1].(BlockGroupWriter)
	if _, err := gow.WriteBlockGroup(true, 500, 3000, []byte("late")); err != nil {
		t.Fatalf("Failed to WriteBlockGroup: '%v'", err)
	}
	for _, w := range ws {
		w.Close()
	}
	<-buf.Closed()

	data := buf.Bytes()

	// The event must be present with its original start time.
	rs, err := NewSimpleBlockReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Failed to create BlockReader: '%v'", err)
	}
	go func() {
		for {
			if _, _, _, err := rs[0].Read(); err != nil {
				return
			}
		}
	}()
	b, keyframe, timestamp, duration, err := rs[1].(BlockGroupReader).ReadBlockGroup()
	if err != nil {
		t.Fatalf("Failed to ReadBlockGroup: '%v'", err)
	}
	if string(b) != "late" || !keyframe || timestamp != 500 || duration != 3000 {
		t.Errorf("Unexpected event: b=%q keyframe=%v timestamp=%v duration=%v",
			b, keyframe, timestamp, duration)
	}

	// The cluster holding the late event must not corrupt the Cues order.
	var result struct {
		Segment cuesTestSegment `ebml:"Segment,size=unknown"`
	}
	if err := ebml.Unmarshal(bytes.NewReader(data), &result); err != nil {
		t.Fatalf("Failed to Unmarshal: '%v'", err)
	}
	if result.Segment.Cues == nil {
		t.Fatal("Cues not found")
	}
	for i := 1; i < len(result.Segment.Cues.CuePoint); i++ {
		prev := result.Segment.Cues.CuePoint[i-1]
		curr := result.Segment.Cues.CuePoint[i]
		if curr.CueTime <= prev.CueTime {
			t.Errorf("CueTime[%d]=%d must be larger than CueTime[%d]=%d",
				i, curr.CueTime, i-1, prev.CueTime)
		}
		if curr.CueTrackPositions[0].CueClusterPosition <= prev.CueTrackPositions[0].CueClusterPosition {
			t.Errorf("CueClusterPosition[%d] must be larger than CueClusterPosition[%d]", i, i-1)
		}
	}
}

func TestBlockWriter_BlockGroupThroughInterceptor(t *testing.T) {
	buf := buffercloser.New()

	sorter, err := NewMultiTrackBlockSorter(WithMaxDelayedPackets(16), WithSortRule(BlockSorterDropOutdated))
	if err != nil {
		t.Fatalf("Failed to create MultiTrackBlockSorter: '%v'", err)
	}
	ws, err := NewSimpleBlockWriter(
		buf,
		[]TrackDescription{
			{TrackNumber: 1, TrackEntry: map[string]interface{}{"TrackNumber": uint(1)}},
			{TrackNumber: 2, TrackEntry: map[string]interface{}{"TrackNumber": uint(2)}},
		},
		WithEBMLHeader(nil), WithSegmentInfo(nil),
		WithBlockInterceptor(sorter),
	)
	if err != nil {
		t.Fatalf("Failed to create BlockWriter: '%v'", err)
	}

	// Audio/video first, then sparse subtitles whose start times are behind
	// the already written audio/video.
	for i := int64(0); i < 50; i++ {
		if _, err := ws[0].Write(true, i*20, []byte{0x01}); err != nil {
			t.Fatalf("Failed to Write: '%v'", err)
		}
	}
	gow := ws[1].(BlockGroupWriter)
	if _, err := gow.WriteBlockGroup(true, 100, 400, []byte("late")); err != nil {
		t.Fatalf("Failed to WriteBlockGroup: '%v'", err)
	}
	for _, w := range ws {
		w.Close()
	}
	<-buf.Closed()

	rs, err := NewSimpleBlockReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Failed to create BlockReader: '%v'", err)
	}

	// Drain the dense track concurrently.
	go func() {
		for {
			if _, _, _, err := rs[0].Read(); err != nil {
				return
			}
		}
	}()

	b, keyframe, timestamp, duration, err := rs[1].(BlockGroupReader).ReadBlockGroup()
	if err != nil {
		t.Fatalf("Failed to ReadBlockGroup: '%v'", err)
	}
	if string(b) != "late" || !keyframe || timestamp != 100 || duration != 400 {
		t.Errorf("Unexpected event: b=%q keyframe=%v timestamp=%v duration=%v",
			b, keyframe, timestamp, duration)
	}
}
