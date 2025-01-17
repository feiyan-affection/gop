/*
 Copyright 2021 The GoPlus Authors (goplus.org)

 Licensed under the Apache License, Version 2.0 (the "License");
 you may not use this file except in compliance with the License.
 You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

 Unless required by applicable law or agreed to in writing, software
 distributed under the License is distributed on an "AS IS" BASIS,
 WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 See the License for the specific language governing permissions and
 limitations under the License.
*/

package parser

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/goplus/gop/ast"
	"github.com/goplus/gop/scanner"
	"github.com/goplus/gop/token"
)

const (
	DbgFlagParseOutput = 1 << iota
	DbgFlagParseError
	DbgFlagAll = DbgFlagParseOutput | DbgFlagParseError
)

var (
	debugParseOutput bool
	debugParseError  bool
)

func SetDebug(dbgFlags int) {
	debugParseOutput = (dbgFlags & DbgFlagParseOutput) != 0
	debugParseError = (dbgFlags & DbgFlagParseError) != 0
}

// -----------------------------------------------------------------------------

// FileSystem represents a file system.
type FileSystem interface {
	ReadDir(dirname string) ([]os.FileInfo, error)
	ReadFile(filename string) ([]byte, error)
	Join(elem ...string) string
}

type localFS struct{}

func (p localFS) ReadDir(dirname string) ([]os.FileInfo, error) {
	return ioutil.ReadDir(dirname)
}

func (p localFS) ReadFile(filename string) ([]byte, error) {
	return ioutil.ReadFile(filename)
}

func (p localFS) Join(elem ...string) string {
	return filepath.Join(elem...)
}

var local FileSystem = localFS{}

// Parse parses a single Go+ source file. The target specifies the Go+ source file.
// If the file couldn't be read, a nil map and the respective error are returned.
func Parse(fset *token.FileSet, target string, src interface{}, mode Mode) (pkgs map[string]*ast.Package, err error) {
	file, err := ParseFile(fset, target, src, mode)
	if err != nil {
		return
	}
	pkgs = make(map[string]*ast.Package)
	pkgs[file.Name.Name] = astFileToPkg(file, target)
	return
}

// astFileToPkg translate ast.File to ast.Package
func astFileToPkg(file *ast.File, fileName string) (pkg *ast.Package) {
	pkg = &ast.Package{
		Name:  file.Name.Name,
		Files: make(map[string]*ast.File),
	}
	pkg.Files[fileName] = file
	return
}

// -----------------------------------------------------------------------------

// ParseDir calls ParseFSDir by passing a local filesystem.
//
func ParseDir(fset *token.FileSet, path string, filter func(os.FileInfo) bool, mode Mode) (pkgs map[string]*ast.Package, first error) {
	return ParseFSDir(fset, local, path, filter, mode)
}

// ParseFSDir calls ParseFile for all files with names ending in ".gop" in the
// directory specified by path and returns a map of package name -> package
// AST with all the packages found.
//
// If filter != nil, only the files with os.FileInfo entries passing through
// the filter (and ending in ".gop") are considered. The mode bits are passed
// to ParseFile unchanged. Position information is recorded in fset, which
// must not be nil.
//
// If the directory couldn't be read, a nil map and the respective error are
// returned. If a parse error occurred, a non-nil but incomplete map and the
// first error encountered are returned.
//
func ParseFSDir(fset *token.FileSet, fs FileSystem, path string, filter func(os.FileInfo) bool, mode Mode) (pkgs map[string]*ast.Package, first error) {
	list, err := fs.ReadDir(path)
	if err != nil {
		return nil, err
	}
	pkgs = make(map[string]*ast.Package)
	for _, d := range list {
		if d.IsDir() {
			continue
		}
		fname := d.Name()
		ext := filepath.Ext(fname)
		ft, isOk := extGopFiles[ext]
		if ft == ast.FileTypeGo && (mode&ParseGoFiles) == 0 {
			isOk = false
		}
		if isOk && !strings.HasPrefix(fname, "_") && (filter == nil || filter(d)) {
			filename := fs.Join(path, fname)
			if filedata, err := fs.ReadFile(filename); err == nil {
				if src, err := ParseFSFile(fset, fs, filename, filedata, mode); err == nil {
					name := src.Name.Name
					pkg, found := pkgs[name]
					if !found {
						pkg = &ast.Package{
							Name:  name,
							Files: make(map[string]*ast.File),
						}
						pkgs[name] = pkg
					}
					pkg.Files[filename] = src
				} else if first == nil {
					first = err
				}
			} else if first == nil {
				first = err
			}
		}
	}
	return
}

var (
	extGopFiles = map[string]ast.FileType{
		".go":  ast.FileTypeGo,
		".gop": ast.FileTypeGop,
		".spx": ast.FileTypeSpx,
		".gmx": ast.FileTypeGmx,
		".spc": ast.FileTypeGmx, // TODO: dynamic register
	}
)

// RegisterFileType registers a new Go+ class file type.
func RegisterFileType(ext string, format ast.FileType) {
	if format != ast.FileTypeSpx && format != ast.FileTypeGmx {
		panic("RegisterFileType: format should be FileTypeSpx or FileTypeGmx")
	}
	if _, ok := extGopFiles[ext]; ok {
		panic("RegisterFileType: file type exists")
	}
	extGopFiles[ext] = format
}

// -----------------------------------------------------------------------------

// ParseFile parses the source code of a single Go+ source file and returns the corresponding ast.File node.
func ParseFile(fset *token.FileSet, filename string, src interface{}, mode Mode) (f *ast.File, err error) {
	return ParseFSFile(fset, local, filename, src, mode)
}

// ParseFSFile parses the source code of a single Go+ source file and returns the corresponding ast.File node.
func ParseFSFile(fset *token.FileSet, fs FileSystem, filename string, src interface{}, mode Mode) (f *ast.File, err error) {
	ext := filepath.Ext(filename)
	ft, isOk := extGopFiles[ext]
	if !isOk {
		ft = ast.FileTypeGop
	}
	return parseFSFileEx(fset, fs, filename, src, mode, ft)
}

func parseFSFileEx(fset *token.FileSet, fs FileSystem, filename string, src interface{}, mode Mode, ft ast.FileType) (f *ast.File, err error) {
	var code []byte
	if src == nil {
		code, err = fs.ReadFile(filename)
	} else {
		code, err = readSource(src)
	}
	if err != nil {
		return
	}
	return parseFileEx(fset, filename, code, mode, ft)
}

// TODO: should not add package info and init|main function.
// If do this, parsing will display error line number when error occur
func parseFileEx(fset *token.FileSet, filename string, code []byte, mode Mode, ft ast.FileType) (f *ast.File, err error) {
	var b bytes.Buffer
	var isMod, noEntrypoint, noPkgDecl bool
	var noEntry *ast.NoEntry_
	var noEntryPos int
	var fsetTmp = token.NewFileSet()
	f, err = parseFile(fsetTmp, filename, code, PackageClauseOnly)
	if err != nil {
		fmt.Fprintf(&b, "package main;%s", code)
		code = b.Bytes()
		noPkgDecl = true
	} else {
		isMod = f.Name.Name != "main"
	}
	_, err = parseFile(fsetTmp, filename, code, mode)
	if err != nil {
		if errlist, ok := err.(scanner.ErrorList); ok {
			if e := errlist[0]; strings.HasPrefix(e.Msg, "expected declaration") {
				var entrypoint string
				switch ft {
				case ast.FileTypeSpx:
					entrypoint = "func Main()"
				case ast.FileTypeGmx:
					entrypoint = "func MainEntry()"
				default:
					if isMod {
						entrypoint = "func init()"
					} else {
						entrypoint = "func main()"
					}
				}
				b.Reset()
				idx := e.Pos.Offset
				fmt.Fprintf(&b, "%s %s{%s\n}", code[:idx], entrypoint, code[idx:])
				code = b.Bytes()
				size := len(entrypoint) + 2
				noEntryPos = idx + size
				noEntry = &ast.NoEntry_{
					Entry: entrypoint,
					Size:  size,
				}
				noEntrypoint = true
				err = nil
			}
		}
	}
	if err == nil {
		f, err = parseFile(fset, filename, code, mode)
		if err == nil {
			if noEntry != nil {
				pos := fset.Position(f.Pos() + token.Pos(noEntryPos))
				noEntry.Line = pos.Line
			}
			f.NoEntrypoint = noEntrypoint
			f.NoEntry_ = noEntry
			f.NoPkgDecl = noPkgDecl
			f.FileType = extGopFiles[filepath.Ext(filename)]
		}
	}
	return
}

var (
	errInvalidSource = errors.New("invalid source")
)

func readSource(src interface{}) ([]byte, error) {
	switch s := src.(type) {
	case string:
		return []byte(s), nil
	case []byte:
		return s, nil
	case *bytes.Buffer:
		// is io.Reader, but src is already available in []byte form
		if s != nil {
			return s.Bytes(), nil
		}
	case io.Reader:
		return ioutil.ReadAll(s)
	}
	return nil, errInvalidSource
}

// -----------------------------------------------------------------------------
