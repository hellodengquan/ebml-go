// Copyright 2026 The ebml-go authors.
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

// Command chunked-webm is an example to write a WebM stream as an
// initialization segment and media segments for browser MSE (Media Source
// Extensions) or DASH/HLS live streaming.
//
// Each media segment is a single Cluster element starting at a video keyframe
// and can be appended to a SourceBuffer independently. The initialization
// segment contains the EBML header, Segment header, Info and Tracks.
package main

import (
	"fmt"
	"os"

	"github.com/at-wat/ebml-go/webm"
)

// discardWriteCloser discards everything written to it.
type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }

func main() {
	tracks := []webm.TrackEntry{
		{
			Name:            "Video",
			TrackNumber:     1,
			TrackUID:        12345,
			CodecID:         "V_VP8",
			TrackType:       1,
			DefaultDuration: 33333333,
			Video: &webm.Video{
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
			Audio: &webm.Audio{
				SamplingFrequency: 48000,
				Channels:          2,
			},
		},
	}

	// The chunks are delivered to the handler as soon as they are complete.
	// In a live server, send them to the client over HTTP or WebSocket.
	handler := func(c webm.Chunk) error {
		kind := "media segment"
		if c.Initialization {
			kind = "initialization segment"
		}
		fmt.Printf("%s: %d bytes, %.3fs-%.3fs\n",
			kind, len(c.Data), float64(c.StartTime)/1000, float64(c.EndTime)/1000)
		return nil
	}

	// The same chunks are also written to stdout. Discard the writer if only
	// the handler is needed.
	ws, err := webm.NewChunkWriter(discardWriteCloser{}, tracks, 1, 0, 1000, handler)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create ChunkWriter: %v\n", err)
		os.Exit(1)
	}

	// Timestamps are in milliseconds (the default TimecodeScale is 1ms).
	// Video keyframes are written every second.
	for i := int64(0); i < 3; i++ {
		tc := i * 1000
		if _, err := ws[0].Write(true, tc, []byte{byte(tc)}); err != nil {
			panic(err)
		}
		for j := int64(1); j <= 3; j++ {
			if _, err := ws[0].Write(false, tc+j*250, []byte{byte(tc + j*250)}); err != nil {
				panic(err)
			}
			if _, err := ws[1].Write(true, tc+j*250, []byte{byte(tc + j*250)}); err != nil {
				panic(err)
			}
		}
	}

	for _, w := range ws {
		if err := w.Close(); err != nil {
			panic(err)
		}
	}
}
