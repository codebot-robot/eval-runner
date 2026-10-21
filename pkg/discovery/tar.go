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

package discovery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ExtractTar extracts an uncompressed or gzip-compressed tar archive into destDir.
func ExtractTar(tarBytes []byte, destDir string) error {
	if len(tarBytes) == 0 {
		return nil
	}

	cleanDest := filepath.Clean(destDir)

	var r io.Reader = bytes.NewReader(tarBytes)
	// Check for gzip magic header (0x1f, 0x8b)
	if len(tarBytes) >= 2 && tarBytes[0] == 0x1f && tarBytes[1] == 0x8b {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer func() {
			if closeErr := gz.Close(); closeErr != nil {
				_ = closeErr
			}
		}()
		r = gz
	}

	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading tar archive: %w", err)
		}

		target := filepath.Join(cleanDest, filepath.Clean(header.Name))
		if !strings.HasPrefix(target, cleanDest+string(filepath.Separator)) && target != cleanDest {
			return fmt.Errorf("tar entry %q escapes destination directory", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("failed to create directory %q: %w", target, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("failed to create parent directory for %q: %w", target, err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, header.FileInfo().Mode())
			if err != nil {
				return fmt.Errorf("failed to open file %q: %w", target, err)
			}
			_, copyErr := io.Copy(f, tr)
			closeErr := f.Close()
			if copyErr != nil {
				return fmt.Errorf("failed to copy tar file content to %q: %w", target, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("failed to close file %q: %w", target, closeErr)
			}
		}
	}
	return nil
}

// CreateTarFromDir creates an uncompressed tar archive containing the files in srcDir,
// excluding .git, .venv, node_modules, and vendor directories.
func CreateTarFromDir(srcDir string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(srcDir, path)
		if err != nil || relPath == "." {
			return nil
		}

		if d.IsDir() && isExcludedDir(d.Name()) {
			return filepath.SkipDir
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relPath)

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if !d.IsDir() {
			if err := func() error {
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer func() {
					if closeErr := f.Close(); closeErr != nil {
						_ = closeErr
					}
				}()
				_, err = io.Copy(tw, f)
				return err
			}(); err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
