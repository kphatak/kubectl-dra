package snapshot

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
)

// LoadFiles builds a snapshot from YAML or JSON files. Each file may hold
// several documents separated by "---", and a document may be a v1 List such
// as the output of "kubectl get ... -o yaml". Kinds kubectl-dra does not use
// are skipped with a warning.
//
// Test fixtures use this, and it also lets a dump taken on one machine be
// analyzed on another.
func LoadFiles(paths ...string) (*Snapshot, error) {
	s := &Snapshot{}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		err = s.read(f)
		_ = f.Close() // read-only, so a close error cannot lose data
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	if len(paths) == 1 {
		s.Source = "file: " + paths[0]
	} else {
		s.Source = fmt.Sprintf("%d files", len(paths))
	}
	s.Sort()
	return s, nil
}

func (s *Snapshot) read(r io.Reader) error {
	docs := utilyaml.NewYAMLReader(bufio.NewReader(r))
	for {
		doc, err := docs.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		if err := s.decode(doc); err != nil {
			return err
		}
	}
}

func (s *Snapshot) decode(data []byte) error {
	obj, gvk, err := scheme.Codecs.UniversalDeserializer().Decode(data, nil, nil)
	if err != nil {
		return err
	}
	if list, ok := obj.(*corev1.List); ok {
		for _, item := range list.Items {
			if err := s.decode(item.Raw); err != nil {
				return err
			}
		}
		return nil
	}
	if !s.Add(obj) {
		s.Warnings = append(s.Warnings, fmt.Sprintf("ignored unsupported kind %s", gvk))
	}
	return nil
}

// ToList returns the snapshot as a single v1 List that LoadFiles can read
// back once serialized. Items carry pre-encoded JSON because RawExtension
// marshals only its Raw field.
func (s *Snapshot) ToList() (*corev1.List, error) {
	objs, err := s.Objects()
	if err != nil {
		return nil, err
	}
	list := &corev1.List{}
	for _, o := range objs {
		raw, err := json.Marshal(o)
		if err != nil {
			return nil, err
		}
		list.Items = append(list.Items, runtime.RawExtension{Raw: raw})
	}
	if err := setTypeMeta(list); err != nil {
		return nil, err
	}
	return list, nil
}
