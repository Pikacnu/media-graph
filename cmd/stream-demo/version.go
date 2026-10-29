package main

import (
	"fmt"
	"io"
	"runtime"

	"github.com/go-gst/go-gst/pkg/gst"
)

// Set by -ldflags at build time so a released binary can report its own tag.
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func printVersion(w io.Writer) {
	fmt.Fprintf(w, "stream-demo %s (commit %s, built %s, %s/%s, %s, gstreamer %s)\n",
		version, commit, buildDate, runtime.GOOS, runtime.GOARCH, runtime.Version(), gst.VersionString())
}