package layout

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrLossy reports a translation between layouts that would land two
// source paths on one target path.
var ErrLossy = errors.New("layout translation loses part of the path")

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
