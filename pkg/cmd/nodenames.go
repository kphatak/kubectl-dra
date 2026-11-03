package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	corev1 "k8s.io/api/core/v1"

	"github.com/kphatak/kubectl-dra/pkg/analyze"
)

// nodeShortener drops the DNS suffix that every node name shares, so table
// output on clusters with long node names stays readable:
// node-1.zone-a.example.com becomes node-1. Pool names usually
// equal node names, so device IDs shrink too. Only the default table
// output is shortened; -o wide, json and yaml keep full names, which is
// what other kubectl commands need.
type nodeShortener struct {
	suffix   string
	replacer *strings.Replacer
}

func newNodeShortener(nodes []*corev1.Node) nodeShortener {
	suffix := commonDomainSuffix(nodes)
	if suffix == "" {
		return nodeShortener{}
	}
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.Name)
	}
	// Longest first, so a name never matches inside a longer one.
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	pairs := make([]string, 0, 2*len(names))
	for _, n := range names {
		pairs = append(pairs, n, strings.TrimSuffix(n, suffix))
	}
	return nodeShortener{suffix: suffix, replacer: strings.NewReplacer(pairs...)}
}

// commonDomainSuffix returns the longest suffix, starting at a dot, that
// every node name ends with and that leaves something in front of it.
// It is empty when there is none.
func commonDomainSuffix(nodes []*corev1.Node) string {
	if len(nodes) == 0 {
		return ""
	}
	common := nodes[0].Name
	for _, n := range nodes[1:] {
		for !strings.HasSuffix(n.Name, common) {
			common = common[1:]
		}
	}
	// Cut to a label boundary, and keep at least one character of every
	// name in front of the suffix.
	for {
		i := strings.IndexByte(common, '.')
		if i < 0 {
			return ""
		}
		common = common[i:]
		ok := true
		for _, n := range nodes {
			if len(n.Name) <= len(common) {
				ok = false
				break
			}
		}
		if ok {
			return common
		}
		common = common[1:]
	}
}

// apply shortens every node name in text.
func (s nodeShortener) apply(text string) string {
	if s.replacer == nil {
		return text
	}
	return s.replacer.Replace(text)
}

// note prints which suffix was dropped, if any.
func (s nodeShortener) note(errOut io.Writer) {
	fmt.Fprintf(errOut, "Node names are shown without %q; use -o wide for full names.\n", s.suffix)
}

// shortWriter shortens node names in everything written to it. It sits in
// front of a tabwriter, so columns are sized for the short names. Each
// write must hold whole names, which fmt.Fprintf calls do.
type shortWriter struct {
	nodeShortener
	w    io.Writer
	used bool
}

func (sw *shortWriter) Write(p []byte) (int, error) {
	out := sw.apply(string(p))
	sw.used = sw.used || len(out) != len(p)
	if _, err := io.WriteString(sw.w, out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// shortenExplanation returns a copy of e with node names shortened in every
// string, and whether anything changed. Going through JSON reaches every
// field, including nested funnels, without listing them; node names never
// contain characters JSON escapes.
func shortenExplanation(e *analyze.Explanation, s nodeShortener) (*analyze.Explanation, bool, error) {
	if s.replacer == nil {
		return e, false, nil
	}
	data, err := json.Marshal(e)
	if err != nil {
		return nil, false, err
	}
	shortened := s.apply(string(data))
	if len(shortened) == len(data) {
		return e, false, nil
	}
	out := &analyze.Explanation{}
	if err := json.Unmarshal([]byte(shortened), out); err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// tableOut returns the writer a command prints its table rows to: tw
// itself for -o wide, otherwise tw behind a node name shortener.
func tableOut(tw *tabwriter.Writer, nodes []*corev1.Node, wide bool) io.Writer {
	s := newNodeShortener(nodes)
	if wide || s.replacer == nil {
		return tw
	}
	return &shortWriter{nodeShortener: s, w: tw}
}

// flushTable flushes the table and, when node names were shortened, says
// so on stderr.
func flushTable(o *Options, tw *tabwriter.Writer, w io.Writer) error {
	if err := tw.Flush(); err != nil {
		return err
	}
	if sw, ok := w.(*shortWriter); ok && sw.used {
		sw.note(o.ErrOut)
	}
	return nil
}
