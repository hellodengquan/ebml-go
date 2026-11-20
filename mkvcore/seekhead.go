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

	"github.com/at-wat/ebml-go"
)

func setSeekHead(header *flexHeader, withCues bool, withTags bool, opts ...ebml.MarshalOption) (segmentDataStart, durationElementPos, tagsSeekPositionPos, tagsSeekPositionLen uint64, err error) {
	infoPos := new(uint64)
	tracksPos := new(uint64)
	header.Segment.SeekHead = &seekHeadFixed{}
	if header.Segment.Info != nil {
		header.Segment.SeekHead.Seek = append(header.Segment.SeekHead.Seek, seekFixed{
			SeekID:       ebml.ElementInfo.Bytes(),
			SeekPosition: infoPos,
		})
	}
	header.Segment.SeekHead.Seek = append(header.Segment.SeekHead.Seek, seekFixed{
		SeekID:       ebml.ElementTracks.Bytes(),
		SeekPosition: tracksPos,
	})
	var cuesPos *uint64
	if withCues {
		cuesPos = new(uint64)
		header.Segment.SeekHead.Seek = append(header.Segment.SeekHead.Seek, seekFixed{
			SeekID:       ebml.ElementCues.Bytes(),
			SeekPosition: cuesPos,
		})
	}
	var tagsPos *uint64
	if withTags {
		tagsPos = new(uint64)
		header.Segment.SeekHead.Seek = append(header.Segment.SeekHead.Seek, seekFixed{
			SeekID:       ebml.ElementTags.Bytes(),
			SeekPosition: tagsPos,
		})
	}

	var segmentPos uint64
	hook := func(e *ebml.Element) {
		switch e.Name {
		case "SeekHead":
			// SeekHead position is the top of the Segment contents.
			// Origin of the segment position is here.
			segmentPos = e.Position
		case "Info":
			*infoPos = e.Position - segmentPos
		case "Tracks":
			*tracksPos = e.Position - segmentPos
		}
	}

	optsWithHook := append([]ebml.MarshalOption{}, opts...)
	optsWithHook = append(optsWithHook, ebml.WithElementWriteHooks(hook))

	var buf bytes.Buffer
	if err := ebml.Marshal(header, &buf, optsWithHook...); err != nil {
		return 0, 0, 0, 0, err
	}

	// The Void (reserved for Cues) starts right after the header.
	// Its position relative to segment data start is the SeekPosition for Cues.
	if cuesPos != nil {
		*cuesPos = uint64(buf.Len()) - segmentPos
	}

	// Find Duration element position by scanning the temporary buffer.
	// We can't use the hook because child element positions don't account
	// for the parent's VINT size (which is written after content for
	// known-size elements). Byte scanning the buffer is exact.
	// Duration element: ID 0x44 0x89, VINT 0x88 (8-byte float64).
	durationPattern := []byte{0x44, 0x89, 0x88}
	if idx := bytes.Index(buf.Bytes(), durationPattern); idx >= 0 {
		durationElementPos = uint64(idx)
	}

	// Find the SeekPosition value of the Tags Seek entry by walking the
	// temporary buffer. The layout of a Seek entry is:
	//   SeekID (0x53AB) + size VINT + Tags ElementID payload (0x1254C367)
	//   SeekPosition (0x53AC) + size VINT + big-endian SeekPosition value
	// The Tags ElementID is a fixed 4-byte sequence that only appears here
	// (as the SeekID payload), and the SeekPosition element ID (0x53AC)
	// follows it once the SeekID size VINT is skipped. Parsing the VINT
	// instead of matching fixed bytes keeps this correct regardless of the
	// active marshal data-size length option. The same header bytes are
	// written to the real output, hence the offset is also valid in the
	// final file.
	if tagsPos != nil {
		tagsSeekPositionPos, tagsSeekPositionLen = findTagsSeekPosition(buf.Bytes())
	}

	return segmentPos, durationElementPos, tagsSeekPositionPos, tagsSeekPositionLen, nil
}

// vintLength returns the total length (1..8) of an EBML VINT starting at the
// given byte, derived from the position of the highest set marker bit.
func vintLength(first byte) int {
	for i := 0; i < 8; i++ {
		if first&(0x80>>i) != 0 {
			return i + 1
		}
	}
	return 8
}

// findTagsSeekPosition locates the absolute offset (within the marshaled
// header) of the big-endian SeekPosition value of the Seek entry that points
// at the Tags element, together with the byte length of that value. It parses
// element VINTs rather than matching fixed byte patterns, so it works with any
// marshal data-size length option. Returns (0, 0) when the entry cannot be
// found.
func findTagsSeekPosition(data []byte) (valuePos, valueLen uint64) {
	tagsID := ebml.ElementTags.Bytes()

	// The Tags ElementID only occurs inside SeekHead as the payload of the
	// SeekID element of the Tags Seek entry.
	idx := bytes.Index(data, tagsID)
	for idx >= 0 {
		// The SeekID element ends right after its payload.
		afterSeekID := idx + len(tagsID)
		if afterSeekID+2 > len(data) {
			return 0, 0
		}
		// Expect the SeekPosition element ID (0x53AC).
		if data[afterSeekID] != 0x53 || data[afterSeekID+1] != 0xAC {
			next := bytes.Index(data[idx+1:], tagsID)
			if next < 0 {
				return 0, 0
			}
			idx = idx + 1 + next
			continue
		}
		vintStart := afterSeekID + 2
		if vintStart >= len(data) {
			return 0, 0
		}
		vintLen := vintLength(data[vintStart])
		valueStart := vintStart + vintLen
		if valueStart >= len(data) {
			return 0, 0
		}
		// The size VINT declares the payload length directly.
		size := readVINTSize(data[vintStart : vintStart+vintLen])
		if valueStart+int(size) > len(data) {
			return 0, 0
		}
		return uint64(valueStart), size
	}
	return 0, 0
}

// readVINTSize decodes the data-size value carried by an EBML size VINT
// (marker bits stripped).
func readVINTSize(vint []byte) uint64 {
	if len(vint) == 0 {
		return 0
	}
	v := uint64(vint[0]) & (0xFF >> uint(len(vint)))
	for _, b := range vint[1:] {
		v = (v << 8) | uint64(b)
	}
	return v
}
