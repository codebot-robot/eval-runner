// Copyright 2026 Google LLC
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

package artifacts

import (
	"bufio"
	"compress/gzip"
	"errors"
	"io"
)

type decompressingReadCloser struct {
	io.Reader
	closers []io.Closer
}

func (d *decompressingReadCloser) Close() error {
	var closeErr error
	for _, closer := range d.closers {
		if closer != nil {
			if err := closer.Close(); err != nil {
				closeErr = errors.Join(closeErr, err)
			}
		}
	}
	return closeErr
}

// NewDecompressingReader peeks at the source to detect gzip magic bytes (0x1f, 0x8b).
// If gzip is detected, it returns a gzip.Reader wrapping the source.
// Otherwise, it returns the source uncompressed without decompression.
func NewDecompressingReader(source io.Reader) (io.ReadCloser, error) {
	bufferedReader := bufio.NewReader(source)
	magic, err := bufferedReader.Peek(2)
	if err == nil && len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gzipReader, err := gzip.NewReader(bufferedReader)
		if err != nil {
			return nil, err
		}
		closers := []io.Closer{gzipReader}
		if closer, ok := source.(io.Closer); ok {
			closers = append(closers, closer)
		}
		return &decompressingReadCloser{
			Reader:  gzipReader,
			closers: closers,
		}, nil
	}
	if closer, ok := source.(io.Closer); ok {
		return &decompressingReadCloser{
			Reader:  bufferedReader,
			closers: []io.Closer{closer},
		}, nil
	}
	return io.NopCloser(bufferedReader), nil
}
