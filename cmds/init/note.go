// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

func readAligned4(r io.Reader, sz int32) ([]byte, error) {
	full := (sz + 3) &^ 3
	data := make([]byte, full)
	_, err := io.ReadFull(r, data)
	if err != nil {
		return nil, err
	}
	data = data[:sz]
	return data, nil
}

func readELFNote(filename, name string, typ int32) ([]byte, error) {
	f, err := elf.Open(filename)
	if err != nil {
		return nil, err
	}
	for _, sect := range f.Sections {
		if sect.Type != elf.SHT_NOTE {
			continue
		}
		r := sect.Open()
		for {
			var namesize, descsize, noteType int32
			err = binary.Read(r, f.ByteOrder, &namesize)
			if err != nil {
				if err == io.EOF {
					break
				}
				return nil, fmt.Errorf("read namesize failed: %v", err)
			}
			err = binary.Read(r, f.ByteOrder, &descsize)
			if err != nil {
				return nil, fmt.Errorf("read descsize failed: %v", err)
			}
			err = binary.Read(r, f.ByteOrder, &noteType)
			if err != nil {
				return nil, fmt.Errorf("read type failed: %v", err)
			}
			noteName, err := readAligned4(r, namesize)
			if err != nil {
				return nil, fmt.Errorf("read name failed: %v", err)
			}
			desc, err := readAligned4(r, descsize)
			if err != nil {
				return nil, fmt.Errorf("read desc failed: %v", err)
			}
			if name == string(noteName) && typ == noteType {
				return desc, nil
			}
		}
	}
	return nil, nil
}

var elfGoNote = []byte("Go\x00\x00")

// The Go build ID is stored in a note described by an ELF PT_NOTE prog
// header. The caller has already opened filename, to get f, and read
// at least 4 kB out, in data.
func readELFGoBuildID(filename string, f *os.File, data []byte) (buildid string, err error) {
	// Assume the note content is in the data, already read.
	// Rewrite the ELF header to set shnum to 0, so that we can pass
	// the data to elf.NewFile and it will decode the Prog list but not
	// try to read the section headers and the string table from disk.
	// That's a waste of I/O when all we care about is the Prog list
	// and the one ELF note.
	switch elf.Class(data[elf.EI_CLASS]) {
	case elf.ELFCLASS32:
		data[48] = 0
		data[49] = 0
	case elf.ELFCLASS64:
		data[60] = 0
		data[61] = 0
	}

	const elfGoBuildIDTag = 4

	ef, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return "", &os.PathError{Path: filename, Op: "parse", Err: err}
	}
	for _, p := range ef.Progs {
		if p.Type != elf.PT_NOTE || p.Filesz < 16 {
			continue
		}

		var note []byte
		if p.Off+p.Filesz < uint64(len(data)) {
			note = data[p.Off : p.Off+p.Filesz]
		} else {
			// For some linkers, such as the Solaris linker,
			// the buildid may not be found in data (which
			// likely contains the first 16kB of the file)
			// or even the first few megabytes of the file
			// due to differences in note segment placement;
			// in that case, extract the note data manually.
			_, err = f.Seek(int64(p.Off), io.SeekStart)
			if err != nil {
				return "", err
			}

			note = make([]byte, p.Filesz)
			_, err = io.ReadFull(f, note)
			if err != nil {
				return "", err
			}
		}

		filesz := p.Filesz
		for filesz >= 16 {
			nameSize := ef.ByteOrder.Uint32(note)
			valSize := ef.ByteOrder.Uint32(note[4:])
			tag := ef.ByteOrder.Uint32(note[8:])
			name := note[12:16]
			if nameSize == 4 && 16+valSize <= uint32(len(note)) && tag == elfGoBuildIDTag && bytes.Equal(name, elfGoNote) {
				return string(note[16 : 16+valSize]), nil
			}

			nameSize = (nameSize + 3) &^ 3
			valSize = (valSize + 3) &^ 3
			notesz := uint64(12 + nameSize + valSize)
			if filesz <= notesz {
				break
			}
			filesz -= notesz
			note = note[notesz:]
		}
	}

	// No note. Treat as successful but build ID empty.
	return "", nil
}

