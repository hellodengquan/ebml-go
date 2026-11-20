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

// seekHeadPositions holds the positions referenced from the SeekHead.
// The SeekHead is written before the elements it points to, so the header is
// marshaled twice: a provisional pass resolves the positions and the final
// pass produces the output. The positions are encoded at a fixed width, so the
// layout is identical in both passes.
type seekHeadPositions struct {
	infoPos   *uint64
	tracksPos *uint64
	cuesPos   *uint64
}

// setSeekHead populates header.Segment.SeekHead and returns the pointers to be
// filled with the element positions.
func setSeekHead(header *flexHeader, withCues bool) *seekHeadPositions {
	pos := &seekHeadPositions{
		infoPos:   new(uint64),
		tracksPos: new(uint64),
	}
	header.Segment.SeekHead = &seekHeadFixed{}
	if header.Segment.Info != nil {
		header.Segment.SeekHead.Seek = append(header.Segment.SeekHead.Seek, seekFixed{
			SeekID:       ebml.ElementInfo.Bytes(),
			SeekPosition: pos.infoPos,
		})
	}
	header.Segment.SeekHead.Seek = append(header.Segment.SeekHead.Seek, seekFixed{
		SeekID:       ebml.ElementTracks.Bytes(),
		SeekPosition: pos.tracksPos,
	})
	if withCues {
		pos.cuesPos = new(uint64)
		header.Segment.SeekHead.Seek = append(header.Segment.SeekHead.Seek, seekFixed{
			SeekID:       ebml.ElementCues.Bytes(),
			SeekPosition: pos.cuesPos,
		})
	}
	return pos
}

// headerLayout is the byte layout of the marshaled EBML header.
type headerLayout struct {
	// segmentStart is the offset of the first byte of the Segment contents,
	// matching the origin of the SeekHead positions.
	segmentStart uint64
	// children maps the ID bytes of a direct child of the Segment to its
	// absolute offset.
	children map[string]uint64
}

func (l headerLayout) child(id []byte) (uint64, bool) {
	p, ok := l.children[string(id)]
	return p, ok
}

// buildHeader marshals header and returns the bytes to write plus the offset of
// the Segment data start and of the Duration element data.
//
// segmentDataStart is the position of the first byte of the Segment contents,
// matching the origin of the SeekHead positions. durationDataPos is the offset
// of the 8-byte float Duration value, or -1 when the header has no Duration.
func buildHeader(
	header *flexHeader, seekHead, withCues bool,
	opts ...ebml.MarshalOption,
) (
	headerBytes []byte, segmentDataStart uint64, durationDataPos int, err error,
) {
	if seekHead {
		positions := setSeekHead(header, withCues)

		// Provisional pass to resolve the positions referenced by the
		// SeekHead. The output is discarded but has the same layout as the
		// final pass.
		var tmp bytes.Buffer
		if err := ebml.Marshal(header, &tmp, opts...); err != nil {
			return nil, 0, 0, err
		}
		layout, ok := parseHeaderLayout(tmp.Bytes())
		if !ok {
			return nil, 0, 0, ErrInvalidHeader
		}
		fillSeekHeadPositions(positions, layout)
		if positions.cuesPos != nil {
			// The Void reserved for Cues starts right after the header.
			*positions.cuesPos = uint64(tmp.Len()) - layout.segmentStart
		}
	}

	var buf bytes.Buffer
	if err := ebml.Marshal(header, &buf, opts...); err != nil {
		return nil, 0, 0, err
	}

	durationDataPos = -1
	layout, ok := parseHeaderLayout(buf.Bytes())
	if !ok {
		return buf.Bytes(), 0, -1, nil
	}
	if infoPos, ok := layout.child(ebml.ElementInfo.Bytes()); ok {
		if pos, ok := findDurationInElement(buf.Bytes(), infoPos); ok {
			durationDataPos = pos
		}
	}
	return buf.Bytes(), layout.segmentStart, durationDataPos, nil
}

// fillSeekHeadPositions copies the relative positions into the SeekHead.
func fillSeekHeadPositions(positions *seekHeadPositions, layout headerLayout) {
	if infoPos, ok := layout.child(ebml.ElementInfo.Bytes()); ok {
		*positions.infoPos = infoPos - layout.segmentStart
	}
	if tracksPos, ok := layout.child(ebml.ElementTracks.Bytes()); ok {
		*positions.tracksPos = tracksPos - layout.segmentStart
	}
}

// parseHeaderLayout parses the EBML header and returns the Segment layout.
func parseHeaderLayout(buf []byte) (headerLayout, bool) {
	layout := headerLayout{children: map[string]uint64{}}

	// Find the Segment element among the top-level elements.
	segPos := -1
	for p := uint64(0); p < uint64(len(buf)); {
		id, _, ok := decodeElementIDAt(buf, p)
		if !ok {
			return layout, false
		}
		_, end, ok := elementContentAt(buf, p)
		if !ok {
			return layout, false
		}
		if bytes.Equal(id, ebml.ElementSegment.Bytes()) {
			segPos = int(p)
			break
		}
		if end <= p {
			return layout, false
		}
		p = end
	}
	if segPos < 0 {
		return layout, false
	}

	bodyStart, bodyEnd, ok := elementContentAt(buf, uint64(segPos))
	if !ok {
		return layout, false
	}
	layout.segmentStart = bodyStart
	for p := bodyStart; p < bodyEnd; {
		id, _, ok := decodeElementIDAt(buf, p)
		if !ok {
			break
		}
		_, end, ok := elementContentAt(buf, p)
		if !ok {
			break
		}
		layout.children[string(id)] = p
		if end <= p || end > bodyEnd {
			break
		}
		p = end
	}
	return layout, true
}

// decodeElementIDAt returns the EBML element ID bytes at pos.
func decodeElementIDAt(buf []byte, pos uint64) (id []byte, length int, ok bool) {
	if pos >= uint64(len(buf)) {
		return nil, 0, false
	}
	idLen := elementIDLen(buf[pos])
	if idLen == 0 || pos+uint64(idLen) > uint64(len(buf)) {
		return nil, 0, false
	}
	return buf[pos : pos+uint64(idLen)], idLen, true
}

// elementContentAt returns the content range of the element at pos.
func elementContentAt(buf []byte, pos uint64) (start, end uint64, ok bool) {
	_, idLen, ok := decodeElementIDAt(buf, pos)
	if !ok {
		return 0, 0, false
	}
	size, sizeLen, ok := decodeSizeVINT(buf[pos+uint64(idLen):])
	if !ok {
		return 0, 0, false
	}
	start = pos + uint64(idLen+sizeLen)
	if size == ebml.SizeUnknown {
		return start, uint64(len(buf)), true
	}
	end = start + size
	if end > uint64(len(buf)) {
		end = uint64(len(buf))
	}
	return start, end, true
}

// elementIDLen returns the length of the EBML element ID starting with b.
func elementIDLen(b byte) int {
	for i := 0; i < 4; i++ {
		if b&(0x80>>uint(i)) != 0 {
			return i + 1
		}
	}
	return 0
}

// findDurationInElement returns the absolute position of the Duration data
// within the Info element that starts at infoPos.
func findDurationInElement(buf []byte, infoPos uint64) (int, bool) {
	contentStart, contentEnd, ok := elementContentAt(buf, infoPos)
	if !ok {
		return 0, false
	}

	durationID := ebml.ElementDuration.Bytes()
	for p := contentStart; p < contentEnd; {
		id, idLen, ok := decodeElementIDAt(buf, p)
		if !ok {
			return 0, false
		}
		size, sizeLen, ok := decodeSizeVINT(buf[p+uint64(idLen):])
		if !ok {
			return 0, false
		}
		dataStart := p + uint64(idLen+sizeLen)
		if dataStart > contentEnd {
			return 0, false
		}
		if bytes.Equal(id, durationID) && size == 8 {
			return int(dataStart), true
		}
		if size == ebml.SizeUnknown {
			return 0, false
		}
		p = dataStart + size
	}
	return 0, false
}

// decodeSizeVINT decodes an EBML variable size integer at the head of b.
func decodeSizeVINT(b []byte) (value uint64, length int, ok bool) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, false
	}
	var mask byte = 0x80
	length = 1
	for b[0]&mask == 0 {
		mask >>= 1
		length++
	}
	if length > 8 || len(b) < length {
		return 0, 0, false
	}
	value = uint64(b[0] & (mask - 1))
	for i := 1; i < length; i++ {
		value = value<<8 | uint64(b[i])
	}
	return value, length, true
}
