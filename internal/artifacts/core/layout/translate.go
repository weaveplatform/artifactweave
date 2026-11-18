package layout

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

// ErrLossy reports a translation between layouts that would land two
// source paths on one target path.
var ErrLossy = errors.New("layout translation loses part of the path")

// TranslateRead rebuilds a virtual download in the member's layout. Unlike a
// copy or move, a read may try a lossy projection: its caller retries the
// original spelling after a miss. Missing layouts or module values retain the
// original path (7.x PathTranslationHelper; the closed Layouts addon is [I]).
func TranslateRead(from, to *Layout, fullPath string) string {
	if strings.TrimSpace(fullPath) == "" || from == nil || to == nil || from.def == to.def {
		return fullPath
	}
	file, checksum := readChecksum(fullPath)
	metadata := ""
	if index := strings.LastIndex(file, ":"); index >= 0 {
		if suffix := file[index+1:]; suffix == "properties" || suffix == "statistics" {
			file, metadata = file[:index], suffix
		}
	}
	v, descriptor := from.readModule(file)
	if v == nil {
		return fullPath
	}
	if v[TokenExt] == "" {
		v[TokenExt] = strings.TrimPrefix(path.Ext(file), ".")
	}
	if v[TokenType] == "" {
		v[TokenType] = v[TokenExt]
	}
	var out string
	var err error
	oneMaven := (from.def == bundled[0]) != (to.def == bundled[0])
	if descriptor && !oneMaven {
		out, err = to.BuildDescriptor(v)
	} else {
		out, err = to.Build(v)
	}
	if err != nil {
		return fullPath
	}
	if metadata != "" {
		out += ":" + metadata
	}
	return out + checksum
}

func (l *Layout) readModule(file string) (Values, bool) {
	for _, descriptor := range []bool{true, false} {
		p := l.artifact
		if descriptor {
			if !l.def.DistinctiveDescriptorPathPattern || l.descriptor == nil {
				continue
			}
			p = l.descriptor
		}
		v, ok := p.match(file)
		if ok && strings.TrimSpace(v[TokenOrg]) != "" && strings.TrimSpace(v[TokenModule]) != "" && strings.TrimSpace(v[TokenBaseRev]) != "" {
			return v, descriptor
		}
	}
	return nil, false
}

func readChecksum(fullPath string) (string, string) {
	for _, suffix := range []string{".sha1", ".md5", ".sha256"} {
		if strings.HasSuffix(fullPath, suffix) {
			return strings.TrimSuffix(fullPath, suffix), suffix
		}
	}
	return fullPath, ""
}

// LossError is ErrLossy for one path: the tokens whose values the target
// layout has no place for, or, when every value survives but the paths
// still meet, another source path that translates to the same target.
type LossError struct {
	From, To string // the layouts' names
	Path     string // the source path
	Target   string // the path the translation would give
	Lost     []string
	Rival    string
}

// Error describes the loss in the first form that applies: the values the
// target layout has no place for (Lost), the other source path that takes
// the same target (Rival), or that the target layout does not read Target
// back. Copy and move report this text to clients.
func (e *LossError) Error() string {
	switch {
	case len(e.Lost) > 0:
		return fmt.Sprintf("layout %s has no place for %s from layout %s, "+
			"so another path could translate to %s", e.To, strings.Join(e.Lost, " or "), e.From,
			e.Target)
	case e.Rival != "":
		return fmt.Sprintf("layout %s gives %s the same path, %s", e.To, e.Rival, e.Target)
	}
	return fmt.Sprintf("layout %s does not read %s back as the path from layout %s",
		e.To, e.Target, e.From)
}

// Unwrap returns ErrLossy, so errors.Is matches every LossError to it.
func (e *LossError) Unwrap() error { return ErrLossy }

// Translate converts path from layout from to layout to, as a copy or move
// between repositories of different layouts does (suppressLayouts=0): the
// values from reads build to's pattern, its descriptor pattern for a
// descriptor. It answers ErrNoMatch when from does not read the path, and
// ErrMissingValue, ErrPattern or ErrUnbuildable when to cannot build one
// from what from reads; the caller keeps such a path as it is.
//
// A translation must never land two source paths on one target path, or
// one version would overwrite another: go-default's v1.2.3-rc.1.zip and
// v1.2.3.zip would both be lib-1.2.3.zip in simple-default, which has no
// file revision. So the target path must lead back to the source path
// alone, or Translate answers a *LossError. The paths that lead back are
// those from builds out of what to reads in the target path, through
// either pattern of each; the source path must be one of them, and none
// of the others may translate to the same target. Two source paths that
// both passed would each be among the other's candidates, so at most one
// passes.
func Translate(from, to *Layout, path string) (string, error) {
	out, err := from.convert(to, path)
	if err != nil {
		return "", err
	}
	loss := &LossError{From: from.def.Name, To: to.def.Name, Path: path, Target: out}
	back := from.candidates(to, out)
	if !slices.Contains(back, path) {
		loss.Lost = from.lost(to, path, out)
		return "", loss
	}
	for _, c := range back {
		if c == path {
			continue
		}
		if other, err := from.convert(to, c); err == nil && other == out {
			loss.Rival = c
			return "", loss
		}
	}
	return out, nil
}

// convert builds path, as l reads it, in to: through to's descriptor
// pattern when path is one of l's descriptors.
func (l *Layout) convert(to *Layout, path string) (string, error) {
	v, err := l.Match(path)
	if err != nil {
		return "", err
	}
	if l.IsDescriptor(path) {
		return to.BuildDescriptor(v)
	}
	return to.Build(v)
}

// candidates are the paths of l that target, a path of to, can have come
// from: what each of to's patterns reads in it, built by each of l's.
// They depend on target alone, which is what makes Translate's test sound.
func (l *Layout) candidates(to *Layout, target string) []string {
	var out []string
	for _, read := range []*pattern{to.artifact, to.descriptor} {
		if read == nil {
			continue
		}
		v, ok := read.match(target)
		if !ok {
			continue
		}
		for _, build := range []*pattern{l.artifact, l.descriptor} {
			if build == nil {
				continue
			}
			if p, err := build.build(v); err == nil && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// lost names the tokens of l's pattern whose values in path the target
// path does not keep, as "[token] value", in the pattern's order. org and
// orgPath are one value under two spellings.
func (l *Layout) lost(to *Layout, path, target string) []string {
	v, _ := l.Match(path)
	kept, err := to.Match(target)
	if err != nil {
		// Every value would read as lost, which says nothing; the error
		// says to does not read its own path back instead.
		return nil
	}
	var names []string
	for _, p := range []*pattern{l.artifact, l.descriptor} {
		if p == nil {
			continue
		}
		for _, n := range p.names {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	var out []string
	for _, n := range names {
		val, ok := v[n]
		if !ok || val == "" {
			continue
		}
		if got, ok := lookup(kept, n); !ok || got != val {
			out = append(out, "["+n+"] "+val)
		}
	}
	return out
}
