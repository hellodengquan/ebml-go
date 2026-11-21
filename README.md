# ebml-go

[![GoDoc](https://godoc.org/github.com/at-wat/ebml-go?status.svg)](http://godoc.org/github.com/at-wat/ebml-go) [![ci](https://github.com/at-wat/ebml-go/actions/workflows/ci.yml/badge.svg)](https://github.com/at-wat/ebml-go/actions/workflows/ci.yml) [![codecov](https://codecov.io/gh/at-wat/ebml-go/branch/master/graph/badge.svg)](https://codecov.io/gh/at-wat/ebml-go) [![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

## A pure Go implementation of bi-directional EBML encoder/decoder

EBML (Extensible Binary Meta Language) is a binary and byte-aligned format that was originally developed for the Matroska audio-visual container.
See https://matroska.org/ for details.

This package implements EBML Marshaler and Unmarshaler for Go.
Currently, commonly used elements of WebM subset is supported.


## Usage

Check out the examples placed under [./examples](./examples/) directory.

API is documented using [GoDoc](http://godoc.org/github.com/at-wat/ebml-go).
EBML can be `Marshal`-ed and `Unmarshal`-ed between tagged struct and binary stream through `io.Reader` and `io.Writer`.

### Chunked (segmented) WebM output

For browser MSE (Media Source Extensions) and DASH/HLS live streaming, the
output can be split into an initialization segment and a sequence of media
segments by passing `mkvcore.WithChunkHandler` (or using
`webm.NewChunkWriter` / `webm.NewSimpleChunkWriter`):

- The initialization segment contains the EBML header, Segment header, Info and
  Tracks.
- Each media segment is a single Cluster element starting at a main track
  keyframe, so it is independently decodable and can be appended to a browser
  `SourceBuffer`.
- Each `Chunk` reports its start and end time. The time ranges of adjacent
  media segments are contiguous and do not overlap.
- Concatenating the initialization segment and all media segments yields a
  regular WebM stream readable by this library.
- Without a chunk handler, the output is the unchanged continuous stream.

See [./examples/chunked-webm](./examples/chunked-webm) for an example.


## References

- [Matroska Container Specifications](https://matroska.org/technical/specs/index.html)
- [WebM Container Guidelines](https://www.webmproject.org/docs/container/)


## License

This package is licensed under [Apache License Version 2.0](./LICENSE).
