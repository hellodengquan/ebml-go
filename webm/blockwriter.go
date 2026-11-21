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
	"io"
	"os"

	"github.com/at-wat/ebml-go/mkvcore"
)

// NewSimpleBlockWriter creates BlockWriteCloser for each track specified as tracks argument.
// Blocks will be written to WebM as EBML SimpleBlocks.
// Resultant WebM is written to given io.WriteCloser and will be closed automatically; don't close it by yourself.
// Frames written to each track must be sorted by their timestamp.
//
// To use the output as an MSE/DASH live stream, pass WithChunkHandler to split
// the output into an initialization segment and media segments.
func NewSimpleBlockWriter(w0 io.WriteCloser, tracks []TrackEntry, opts ...mkvcore.BlockWriterOption) ([]BlockWriteCloser, error) {
	trackDesc := []mkvcore.TrackDescription{}
	for _, t := range tracks {
		trackDesc = append(trackDesc,
			mkvcore.TrackDescription{
				TrackNumber: t.TrackNumber,
				TrackEntry:  t,
			})
	}
	options := []mkvcore.BlockWriterOption{
		mkvcore.WithEBMLHeader(DefaultEBMLHeader),
		mkvcore.WithSegmentInfo(DefaultSegmentInfo),
		mkvcore.WithBlockInterceptor(DefaultBlockInterceptor),
	}
	options = append(options, opts...)
	ws, err := mkvcore.NewSimpleBlockWriter(w0, trackDesc, options...)
	webmWs := []BlockWriteCloser{}
	for _, w := range ws {
		webmWs = append(webmWs, BlockWriteCloser(w))
	}
	return webmWs, err
}

// NewChunkWriter creates BlockWriteCloser for each track specified as tracks argument
// and splits the output into an initialization segment and media segments.
//
// It is a shorthand of NewSimpleBlockWriter with WithChunkHandler and a main
// track configuration. The initialization segment and each complete media
// segment are passed to handler; see mkvcore.WithChunkHandler for the delivery
// and segmentation contract. The chunks are also written to w0, so that
// concatenating all of them yields a regular WebM stream readable by this
// library.
//
// mainTrackNumber is the track number of the main (usually video) track.
// Each media segment starts at a keyframe of it.
// minDuration and maxDuration are the preferred minimum and maximum media
// segment duration in the timecode scale.
// See mkvcore.WithMinMaxClusterDuration for their semantics.
//
// WithChunkHandler in opts is overridden by handler.
// WithCues is not available in chunked output; io.WriteCloser argument is
// closed automatically; don't close it by yourself.
func NewChunkWriter(
	w0 io.WriteCloser, tracks []TrackEntry, mainTrackNumber uint64,
	minDuration, maxDuration int64, handler mkvcore.ChunkHandler,
	opts ...mkvcore.BlockWriterOption,
) ([]BlockWriteCloser, error) {
	options := []mkvcore.BlockWriterOption{
		mkvcore.WithMinMaxClusterDuration(mainTrackNumber, minDuration, maxDuration),
		mkvcore.WithChunkHandler(handler),
	}
	options = append(options, opts...)
	return NewSimpleBlockWriter(w0, tracks, options...)
}

// NewSimpleChunkWriter creates BlockWriteCloser for each track specified as
// tracks argument and splits the output into an initialization segment and
// media segments.
//
// It is a shorthand of NewSimpleBlockWriter with WithChunkHandler.
// The main track and media segment duration are inferred or configured by
// opts. If not configured, the first video track is used as the main track and
// the default maximum cluster duration is used.
// The default block interceptor and default WebM header/info are applied.
//
// See mkvcore.WithChunkHandler for the segmentation contract.
func NewSimpleChunkWriter(
	w0 io.WriteCloser, tracks []TrackEntry, handler mkvcore.ChunkHandler,
	opts ...mkvcore.BlockWriterOption,
) ([]BlockWriteCloser, error) {
	options := []mkvcore.BlockWriterOption{
		mkvcore.WithChunkHandler(handler),
	}
	options = append(options, opts...)
	return NewSimpleBlockWriter(w0, tracks, options...)
}

// NewSimpleWriter creates BlockWriteCloser for each track specified as tracks argument.
// Blocks will be written to WebM as EBML SimpleBlocks.
// Resultant WebM is written to given io.WriteCloser.
// io.WriteCloser will be closed automatically; don't close it by yourself.
//
// Deprecated: This is exposed to keep compatibility with the old version.
// Use NewSimpleBlockWriter instead.
func NewSimpleWriter(w0 io.WriteCloser, tracks []TrackEntry, opts ...mkvcore.BlockWriterOption) ([]*FrameWriter, error) {
	os.Stderr.WriteString(
		"Deprecated: You are using deprecated webm.NewSimpleWriter and *webm.blockWriter.\n" +
			"            Use webm.NewSimpleBlockWriter and webm.BlockWriteCloser interface instead.\n" +
			"            See https://godoc.org/github.com/at-wat/ebml-go to find out the latest API.\n",
	)
	ws, err := NewSimpleBlockWriter(w0, tracks, opts...)
	var ws2 []*FrameWriter
	for _, w := range ws {
		ws2 = append(ws2, &FrameWriter{w})
	}
	return ws2, err
}
