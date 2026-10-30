package docker

import (
	"bytes"
	"strings"
	"testing"

	"github.com/docker/docker/pkg/stdcopy"
)

// Builds a multiplexed docker stream from stdout and stderr chunks
func muxStream(t *testing.T, stdout, stderr []string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	out := stdcopy.NewStdWriter(&buf, stdcopy.Stdout)
	errw := stdcopy.NewStdWriter(&buf, stdcopy.Stderr)
	for i := 0; i < len(stdout) || i < len(stderr); i++ {
		if i < len(stdout) {
			if _, err := out.Write([]byte(stdout[i])); err != nil {
				t.Fatal(err)
			}
		}
		if i < len(stderr) {
			if _, err := errw.Write([]byte(stderr[i])); err != nil {
				t.Fatal(err)
			}
		}
	}
	return &buf
}

func TestReadExecOutputPassesSmallStreams(t *testing.T) {
	stream := muxStream(t, []string{"one\n", "two\n"}, []string{"warn\n"})
	stdout, stderr, err := readExecOutput(stream)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "one\ntwo\n" || stderr != "warn\n" {
		t.Fatalf("stdout %q stderr %q", stdout, stderr)
	}
}

func TestReadExecOutputCapsEachStream(t *testing.T) {
	chunk := strings.Repeat("x", 256<<10)
	var stdout []string
	for i := 0; i < 12; i++ {
		stdout = append(stdout, chunk)
	}
	stdout = append(stdout, "END")
	stream := muxStream(t, stdout, []string{"small"})
	gotOut, gotErr, err := readExecOutput(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotOut, ExecTruncatedPrefix) {
		t.Fatal("capped stdout must carry the truncation prefix")
	}
	body := strings.TrimPrefix(gotOut, ExecTruncatedPrefix)
	if len(body) != MaxExecOutputBytes || !strings.HasSuffix(body, "END") {
		t.Fatalf("retained %d bytes ending %q, want %d ending END", len(body), body[len(body)-3:], MaxExecOutputBytes)
	}
	if gotErr != "small" {
		t.Fatalf("stderr %q must stay untouched", gotErr)
	}
}
