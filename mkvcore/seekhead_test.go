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

package mkvcore

import "testing"

func TestVintLength(t *testing.T) {
	cases := map[byte]int{
		0x80: 1,
		0xFF: 1,
		0x40: 2,
		0x7F: 2,
		0x20: 3,
		0x10: 4,
		0x08: 5,
		0x04: 6,
		0x02: 7,
		0x01: 8,
	}
	for first, want := range cases {
		if got := vintLength(first); got != want {
			t.Errorf("vintLength(0x%02X): got %d, want %d", first, got, want)
		}
	}
}

func TestFindTagsSeekPosition(t *testing.T) {
	// Construct a minimal SeekHead-like buffer with two Seek entries:
	// one for Tracks, one for Tags, using both default (8-byte) and short
	// (2-byte) SeekPosition VINTs.
	tagsID := []byte{0x12, 0x54, 0xC3, 0x67}
	tracksID := []byte{0x16, 0x54, 0xAE, 0x6B}

	// build constructs two Seek entries. seekPosVINT is the full SeekPosition
	// size VINT, and seekPosValue is the payload following it (its length must
	// equal the size declared by the VINT).
	build := func(seekPosVINT, seekPosValue []byte) []byte {
		var b []byte
		// Seek entry for Tracks: SeekID (4-byte payload) + SeekPosition.
		b = append(b, 0x4D, 0xBB, 0x84) // Seek element is implied; just SeekID
		b = append(b, 0x53, 0xAB, 0x84)
		b = append(b, tracksID...)
		b = append(b, 0x53, 0xAC)
		b = append(b, seekPosVINT...)
		b = append(b, seekPosValue...)
		// Seek entry for Tags.
		b = append(b, 0x53, 0xAB, 0x84)
		b = append(b, tagsID...)
		b = append(b, 0x53, 0xAC)
		b = append(b, seekPosVINT...)
		b = append(b, seekPosValue...)
		return b
	}

	eightByteValue := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x23}

	t.Run("Default8ByteVINT", func(t *testing.T) {
		data := build([]byte{0x88}, eightByteValue)
		gotPos, gotLen := findTagsSeekPosition(data)
		wantPos := uint64(len(data) - 8)
		if gotPos != wantPos || gotLen != 8 {
			t.Errorf("got (%d, %d), want (%d, 8)", gotPos, gotLen, wantPos)
		}
	})

	t.Run("TwoByteVINT", func(t *testing.T) {
		// A 2-byte size VINT 0x40 0x08 declares an 8-byte payload, as the
		// encoder emits under WithDataSizeLen(2) for a size=8 field.
		data := build([]byte{0x40, 0x08}, eightByteValue)
		gotPos, gotLen := findTagsSeekPosition(data)
		wantPos := uint64(len(data) - 8)
		if gotPos != wantPos || gotLen != 8 {
			t.Errorf("got (%d, %d), want (%d, 8)", gotPos, gotLen, wantPos)
		}
	})

	t.Run("OneByteVINT", func(t *testing.T) {
		// A 1-byte size VINT 0x81 declares a one-byte payload.
		data := build([]byte{0x81}, []byte{0x10})
		gotPos, gotLen := findTagsSeekPosition(data)
		wantPos := uint64(len(data) - 1)
		if gotPos != wantPos || gotLen != 1 {
			t.Errorf("got (%d, %d), want (%d, 1)", gotPos, gotLen, wantPos)
		}
	})

	t.Run("NotFound", func(t *testing.T) {
		gotPos, gotLen := findTagsSeekPosition([]byte{0x00, 0x01, 0x02})
		if gotPos != 0 || gotLen != 0 {
			t.Errorf("expected (0, 0) for buffer without Tags ID, got (%d, %d)", gotPos, gotLen)
		}
	})
}

func TestReadVINTSize(t *testing.T) {
	cases := map[string]struct {
		vint []byte
		want uint64
	}{
		"OneByte":   {[]byte{0x88}, 8},
		"TwoByte":   {[]byte{0x41, 0x10}, 0x110},
		"FourByte":  {[]byte{0x10, 0x00, 0x00, 0x2A}, 0x2A},
		"EightByte": {[]byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05}, 5},
	}
	for name, c := range cases {
		c := c
		t.Run(name, func(t *testing.T) {
			if got := readVINTSize(c.vint); got != c.want {
				t.Errorf("readVINTSize(% X): got %d, want %d", c.vint, got, c.want)
			}
		})
	}
}
