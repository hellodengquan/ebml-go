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

// BlockWriter is a Matroska block writer interface.
type BlockWriter interface {
	// Write a block to the connected Matroska writer.
	// timestamp is in millisecond.
	Write(keyframe bool, timestamp int64, b []byte) (int, error)
}

// BlockGroupWriter is a BlockWriter which can also write a block with its
// display duration. Blocks written through WriteBlockGroup are stored as
// BlockGroup with BlockDuration instead of SimpleBlock.
// This is used for sparse tracks, e.g. WebVTT subtitle tracks, whose each
// event has its own display duration.
type BlockGroupWriter interface {
	BlockWriter
	// WriteBlockGroup writes a block to the connected Matroska writer as a
	// BlockGroup. timestamp and duration are in millisecond.
	// A positive duration is stored as BlockDuration; duration 0 leaves
	// BlockDuration unset. Negative duration is rejected with
	// ErrInvalidBlockDuration.
	// A non-keyframe block is stored with ReferenceBlock so that it is
	// recognized as not independently decodable on reading.
	WriteBlockGroup(keyframe bool, timestamp, duration int64, b []byte) (int, error)
}

// BlockReader is a Matroska block reader interface.
type BlockReader interface {
	// Read a block from the connected Matroska reader.
	Read() (b []byte, keyframe bool, timestamp int64, err error)
}

// BlockGroupReader is a BlockReader which can also read the display duration
// of a BlockGroup.
type BlockGroupReader interface {
	BlockReader
	// ReadBlockGroup reads a block from the connected Matroska reader.
	// duration is the BlockDuration of the BlockGroup in millisecond, or 0
	// when the block has no BlockDuration (e.g. SimpleBlock).
	// keyframe is true when the block can be decoded independently, i.e.
	// SimpleBlock with its keyframe flag set or BlockGroup without
	// ReferenceBlock.
	ReadBlockGroup() (b []byte, keyframe bool, timestamp, duration int64, err error)
}

// BlockCloser is a Matroska closer interface.
type BlockCloser interface {
	// Close the stream frame writer.
	// Output Matroska will be closed after closing all FrameWriter.
	Close() error
}

// BlockWriteCloser groups Writer, BlockGroupWriter and Closer.
type BlockWriteCloser interface {
	BlockGroupWriter
	BlockCloser
}

// BlockReadCloser groups Reader, BlockGroupReader and Closer.
type BlockReadCloser interface {
	BlockGroupReader
	BlockCloser
}

// TrackEntryGetter is a interface to get TrackEntry.
type TrackEntryGetter interface {
	TrackEntry() TrackEntry
}

// BlockReadCloserWithTrackEntry groups BlockReadCloser and TrackEntryGetter.
type BlockReadCloserWithTrackEntry interface {
	BlockReadCloser
	TrackEntryGetter
}
