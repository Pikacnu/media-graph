package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-gst/go-gst/pkg/gst"
)

func TestCLICommandDispatch(t *testing.T) {
	for _, command := range [][]string{nil, {"help"}, {"-h"}, {"--help"}, {"version"}, {"inspect"}, {"inspect", "TestSource"}} {
		t.Run(stringOrHelp(command), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := run(command, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			if stdout.Len() == 0 {
				t.Fatal("command produced no output")
			}
		})
	}
}

func TestCLISubcommandHelp(t *testing.T) {
	for _, command := range []string{"apply", "run", "serve", "preview", "check", "inspect", "version"} {
		for _, option := range []string{"-h", "--help"} {
			t.Run(command+option, func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				if err := run([]string{command, option}, &stdout, &stderr); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(stdout.String()+stderr.String(), "Usage") {
					t.Fatal("missing usage output")
				}
			})
		}
	}
	if err := run([]string{"serve", "-not-a-flag"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("unknown flag unexpectedly succeeded")
	}
}

func TestCLIRunCustomNodeIDs(t *testing.T) {
	gst.Init()
	data, err := json.Marshal(DemoGraph())
	if err != nil {
		t.Fatal(err)
	}
	custom := strings.ReplaceAll(strings.ReplaceAll(string(data), "demo_input", "source"), "demo_output", "output")
	graphPath := writeGraphFile(t, custom, ".json")
	inputPath := filepath.Join(t.TempDir(), "frame.raw")
	if err := os.WriteFile(inputPath, make([]byte, 12), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"run", "-graph", graphPath, "-input", inputPath, "-input-node", "source", "-output-node", "output", "-settle", "2s"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "input_bytes=12 output_bytes=12") {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func stringOrHelp(args []string) string {
	if len(args) == 0 {
		return "no-arguments"
	}
	return args[0]
}

func TestCLIRejectsInvalidRequests(t *testing.T) {
	t.Setenv("STREAM_DEMO_ENDPOINT", "")
	for _, command := range [][]string{{"unknown"}, {"status"}, {"check"}, {"apply"}, {"serve"}, {"preview"}, {"inspect", "DoesNotExist"}, {"inspect", "a", "b"}} {
		t.Run(command[0], func(t *testing.T) {
			if err := run(command, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
				t.Fatal("invalid command unexpectedly succeeded")
			}
		})
	}
}

func TestCLIExampleGraphsValidate(t *testing.T) {
	for _, path := range []string{"../../examples/source-parse-output.json", "../../examples/source-parse-output.yaml", "../../examples/test-source-preview.yaml"} {
		t.Run(path, func(t *testing.T) {
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			if err := run([]string{"check", "-graph", path}, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(stdout.Bytes(), []byte("valid graph=")) {
				t.Fatalf("unexpected output: %s", stdout.String())
			}
		})
	}
}
