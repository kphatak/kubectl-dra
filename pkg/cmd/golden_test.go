package cmd

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/kphatak/kubectl-dra/pkg/snapshot"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

// assertGolden compares got with testdata/<name>.golden. Run
// "go test ./pkg/cmd -update" to rewrite the files after an intended change,
// then review the diff.
func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./pkg/cmd -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from %s; rerun with -update if the change is intended\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

// testOptions returns Options writing to buffers instead of the terminal.
func testOptions(output string) (o *Options, out, errOut *bytes.Buffer) {
	streams, _, out, errOut := genericiooptions.NewTestIOStreams()
	return &Options{output: output, IOStreams: streams}, out, errOut
}

func loadFixtures(t *testing.T, names ...string) *snapshot.Snapshot {
	t.Helper()
	paths := make([]string, len(names))
	for i, n := range names {
		paths[i] = filepath.Join("..", "..", "test", "fixtures", n)
	}
	s, err := snapshot.LoadFiles(paths...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
