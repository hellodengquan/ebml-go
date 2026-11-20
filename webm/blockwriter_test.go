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

// seekableBuffer is an in-memory io.WriteCloser + io.WriteSeeker for tests
// that need header rewriting.
type seekableBuffer struct {
	buf    []byte
	pos    int
	closed chan struct{}
}

func (b *seekableBuffer) Write(p []byte) (int, error) {
	end := b.pos + len(p)
	if end > len(b.buf) {
		b.buf = append(b.buf, make([]byte, end-len(b.buf))...)
	}
	copy(b.buf[b.pos:], p)
	b.pos = end
	return len(p), nil
}

func (b *seekableBuffer) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		b.pos = int(offset)
	case io.SeekCurrent:
		b.pos += int(offset)
	case io.SeekEnd:
		b.pos = len(b.buf) + int(offset)
	}
	return int64(b.pos), nil
}

func (b *seekableBuffer) Close() error {
	close(b.closed)
	return nil
}

func (b *seekableBuffer) bytes() []byte {
	return b.buf
}

func TestBlockWriter(t *testing.T) {
	buf := buffercloser.New()

	tracks := []TrackEntry{
		{
			Name:        "Video",
			TrackNumber: 1,
			TrackUID:    12345,
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
			TrackUID:    54321,
			CodecID:     "A_OPUS",
			TrackType:   2,
			Audio: &Audio{
				SamplingFrequency: 48000.0,
				Channels:          2,
			},
		},
	}
	ws, err := NewSimpleBlockWriter(buf, tracks)
	if err != nil {
		t.Fatalf("Failed to create BlockWriter: %v", err)
	}

	if len(ws) != len(tracks) {
		t.Fatalf("Number of the returned writer (%d) must be same as the number of TrackEntry (%d)", len(ws), len(tracks))
	}

	if n, err := ws[0].Write(false, 100, []byte{0x01, 0x02}); err != nil {
		t.Fatalf("Failed to Write: %v", err)
	} else if n != 2 {
		t.Errorf("Expected return value of BlockWriter.Write: 2, got: %d", n)
	}

	if n, err := ws[1].Write(true, 110, []byte{0x03, 0x04, 0x05}); err != nil {
		t.Fatalf("Failed to Write: %v", err)
	} else if n != 3 {
		t.Errorf("Expected return value of BlockWriter.Write: 3, got: %d", n)
	}

	// Ignored due to old timestamp
	if n, err := ws[0].Write(true, -32769, []byte{0x0A}); err != nil {
		t.Fatalf("Failed to Write: %v", err)
	} else if n != 1 {
		t.Errorf("Expected return value of BlockWriter.Write: 1, got: %d", n)
	}

	if n, err := ws[0].Write(true, 130, []byte{0x06}); err != nil {
		t.Fatalf("Failed to Write: %v", err)
	} else if n != 1 {
		t.Errorf("Expected return value of BlockWriter.Write: 1, got: %d", n)
	}

	ws[0].Close()
	ws[1].Close()
	select {
	case <-buf.Closed():
	default:
		t.Errorf("Base io.WriteCloser is not closed by BlockWriter")
	}

	expected := struct {
		Header  EBMLHeader `ebml:"EBML"`
		Segment Segment    `ebml:"Segment,size=unknown"`
	}{
		Header: *DefaultEBMLHeader,
		Segment: Segment{
			Info: *DefaultSegmentInfo,
			Tracks: Tracks{
				TrackEntry: tracks,
			},
			Cluster: []Cluster{
				{
					Timecode: uint64(0),
					SimpleBlock: []ebml.Block{
						{
							TrackNumber: 1,
							Timecode:    int16(0),
							Keyframe:    false,
							Data:        [][]byte{{0x01, 0x02}},
						},
						{
							TrackNumber: 2,
							Timecode:    int16(10),
							Keyframe:    true,
							Data:        [][]byte{{0x03, 0x04, 0x05}},
						},
						{
							TrackNumber: 1,
							Timecode:    int16(30),
							Keyframe:    true,
							Data:        [][]byte{{0x06}},
						},
					},
				},
				{
					Timecode: uint64(30),
					PrevSize: uint64(39),
				},
			},
		},
	}
	var result struct {
		Header  EBMLHeader `ebml:"EBML"`
		Segment Segment    `ebml:"Segment,size=unknown"`
	}
	if err := ebml.Unmarshal(bytes.NewReader(buf.Bytes()), &result); err != nil {
		t.Fatalf("Failed to Unmarshal resultant binary: %v", err)
	}
	if !reflect.DeepEqual(expected, result) {
		t.Errorf("Unexpected WebM data,\nexpected: %+v\n     got: %+v", expected, result)
	}
}

func TestBlockWriter_WithTrackStats(t *testing.T) {
	buf := &seekableBuffer{closed: make(chan struct{})}

	tracks := []TrackEntry{
		{
			Name:        "Video",
			TrackNumber: 1,
			TrackUID:    12345,
			CodecID:     "V_VP8",
			TrackType:   1,
			Video:       &Video{PixelWidth: 320, PixelHeight: 240},
		},
		{
			Name:        "Audio",
			TrackNumber: 2,
			TrackUID:    54321,
			CodecID:     "A_OPUS",
			TrackType:   2,
			Audio:       &Audio{SamplingFrequency: 48000, Channels: 2},
		},
	}
	ws, err := NewSimpleBlockWriter(buf, tracks,
		mkvcore.WithSeekHead(true),
		mkvcore.WithTrackStats(true),
	)
	if err != nil {
		t.Fatalf("Failed to create BlockWriter: %v", err)
	}

	if _, err := ws[0].Write(true, 0, []byte{0x01, 0x02, 0x03, 0x04}); err != nil {
		t.Fatalf("Failed to Write: %v", err)
	}
	if _, err := ws[1].Write(true, 10, []byte{0x09, 0x09}); err != nil {
		t.Fatalf("Failed to Write: %v", err)
	}
	if _, err := ws[0].Write(false, 33, []byte{0x05}); err != nil {
		t.Fatalf("Failed to Write: %v", err)
	}
	ws[0].Close()
	ws[1].Close()
	<-buf.closed

	var result struct {
		Segment struct {
			SeekHead *struct {
				Seek []struct {
					SeekID       []byte `ebml:"SeekID"`
					SeekPosition uint64 `ebml:"SeekPosition,size=8"`
				} `ebml:"Seek"`
			} `ebml:"SeekHead"`
			Tags *struct {
				Tag []struct {
					Targets struct {
						TagTrackUID []uint64 `ebml:"TagTrackUID"`
					} `ebml:"Targets"`
					SimpleTag []struct {
						TagName   string `ebml:"TagName"`
						TagString string `ebml:"TagString"`
					} `ebml:"SimpleTag"`
				} `ebml:"Tag"`
			} `ebml:"Tags"`
			Cluster []struct {
				SimpleBlock []ebml.Block `ebml:"SimpleBlock"`
			} `ebml:"Cluster,size=unknown"`
		} `ebml:"Segment,size=unknown"`
	}
	if err := ebml.Unmarshal(bytes.NewReader(buf.bytes()), &result); err != nil {
		t.Fatalf("Failed to Unmarshal: %v", err)
	}

	if result.Segment.Tags == nil || len(result.Segment.Tags.Tag) != 2 {
		t.Fatalf("Expected 2 Tag elements, got %+v", result.Segment.Tags)
	}

	want := map[uint64]map[string]string{
		12345: {
			"NUMBER_OF_FRAMES": "2",
			"NUMBER_OF_BYTES":  "5",
			"DURATION":         "00:00:00.033000000",
		},
		54321: {
			"NUMBER_OF_FRAMES": "1",
			"NUMBER_OF_BYTES":  "2",
			"DURATION":         "00:00:00.000000000",
		},
	}
	for _, tg := range result.Segment.Tags.Tag {
		uid := tg.Targets.TagTrackUID[0]
		for _, st := range tg.SimpleTag {
			if st.TagString != want[uid][st.TagName] {
				t.Errorf("UID %d %s: got %q, want %q", uid, st.TagName, st.TagString, want[uid][st.TagName])
			}
		}
	}

	// Tags must be reachable from the SeekHead.
	found := false
	for _, seek := range result.Segment.SeekHead.Seek {
		if bytes.Equal(seek.SeekID, ebml.ElementTags.Bytes()) {
			found = true
		}
	}
	if !found {
		t.Error("SeekHead has no Tags entry")
	}

	// Playback data remains present.
	if len(result.Segment.Cluster) == 0 || len(result.Segment.Cluster[0].SimpleBlock) != 3 {
		t.Error("Expected the 3 SimpleBlocks to remain playable")
	}
}

func TestBlockWriter_NewSimpleWriter(t *testing.T) {
	buf := buffercloser.New()

	tracks := []TrackEntry{
		{
			TrackNumber: 1,
			TrackUID:    2,
			CodecID:     "",
			TrackType:   1,
		},
	}

	// Check old API
	var ws []*FrameWriter
	var err error

	ws, err = NewSimpleWriter(
		buf, tracks,
		mkvcore.WithEBMLHeader(nil),
		mkvcore.WithSegmentInfo(nil),
		mkvcore.WithSeekHead(false),
	)
	if err != nil {
		t.Fatalf("Failed to create BlockWriter: %v", err)
	}

	if len(ws) != 1 {
		t.Fatalf("Number of the returned writer must be 1, but got %d", len(ws))
	}
	ws[0].Close()

	expectedBytes := []byte{
		0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
		0x16, 0x54, 0xAE, 0x6B, 0x8E,
		0xAE, 0x8C,
		0xD7, 0x81, 0x01,
		0x73, 0xC5, 0x81, 0x02,
		0x86, 0x80,
		0x83, 0x81, 0x01,
		0x1F, 0x43, 0xB6, 0x75, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
		0xE7, 0x81, 0x00,
	}
	if !bytes.Equal(buf.Bytes(), expectedBytes) {
		t.Errorf("Unexpected WebM binary,\nexpected: %+v\n     got: %+v", expectedBytes, buf.Bytes())
	}
}
