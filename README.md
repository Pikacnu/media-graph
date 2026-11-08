# Stream Things Graph Demo

This project is an attempt to build a fully functional graph-like system with a simplified node model.

It is intended to include a frontend, although the frontend has not been implemented yet. The goal is to help streamers get started, build and edit their streams, and publish them to different platforms.

The project is not finished yet. However, the backend graph system is already close to an MVP state. This repository includes a simple CLI and several examples so you can get started and see how the underlying system works.

## What this demo shows

```bash
JSON/YAML graph
  → node factory lookup
  → MediaGraph.ApplyGraph
  → GStreamer pipeline
  → typed input/output handles
  → live snapshot and graph update
  → optional browser MJPEG preview
```

The showcase is intentionally local. RTMP, SRT, WHIP, frontend integration, full audio mixing, and production deployment are outside this focused demo.

## Requirements

- Go 1.26.4
- GStreamer 1.26 or newer for local builds
- Docker or Podman for the container image

## Quick start

```bash
go run ./cmd/stream-demo inspect
go run ./cmd/stream-demo check -graph ./examples/source-parse-output.yaml
go run ./cmd/stream-demo run -input ./frame.raw
go run ./cmd/stream-demo serve -graph ./examples/source-parse-output.yaml
go run ./cmd/stream-demo version
```

### Browser video preview

For a self-running moving test pattern (no input file needed), run from the project root:

```powershell
go run .\cmd\stream-demo serve -graph .\examples\test-source-preview.yaml -listen 127.0.0.1:8091 -output-node demo_output -width 640 -height 360
```

In a second PowerShell terminal, open the video:

```powershell
Start-Process "http://127.0.0.1:8091/stream"
```

Stop any existing server on port 8091 before starting this example. Keep the server running
until finished; press Ctrl+C to stop it. The width and height flags must match the graph's
raw RGB output dimensions.

`/stream` shows MJPEG video; `/preview` returns graph JSON, and the CLI `preview` command
prints a graph summary. `/graph` accepts POST updates only, not browser GET requests.
The source/parse examples above wait for external input and do not generate video themselves.

For the full CLI tutorial, see [docs/CLI.md](./docs/CLI.md).

## AI-Assisted CLI Demo Development

AI tools assisted with:

- learning and applying the GStreamer and Go APIs used by the project;
- helping the CLI showcase and demo workflows;
- exploring and explaining the graph architecture and design;
- summarizing and improving project documentation;
- debugging Go, GStreamer, build, test, dependency, and codebase issues;
- preparing the Dockerfile, GitHub Actions workflow, and examples.

## Build and image

```bash
go test ./...
go build -o ./dist/stream-demo ./cmd/stream-demo
docker build -t stream-things-demo:local .
```

GitHub Actions builds the executable artifact and exports a Docker/OCI image tarball. On every
`v*` tag push, the workflow publishes a GitHub Release containing both assets plus `SHA256SUMS`.

## Release assets

The demo release contains:

- `stream-demo-linux-amd64`: Linux x86-64 executable.
- `stream-things-demo-image.tar.gz`: OCI/Docker image archive.
- `SHA256SUMS`: checksums for both assets.

Download a release executable and run it directly:

```bash
chmod +x ./stream-demo-linux-amd64
./stream-demo-linux-amd64 version
./stream-demo-linux-amd64 inspect
./stream-demo-linux-amd64 serve -graph ./examples/source-parse-output.yaml
```

`version` prints the release tag, commit, build timestamp, Go toolchain, and the GStreamer
runtime version. Local builds without `-ldflags` report `dev`.

Load the image archive into Docker:

```bash
docker load --input ./stream-things-demo-image.tar.gz
docker run --rm -p 8091:8091 stream-things-demo:release
```

The image exposes the same local control plane on port 8091:
`/healthz`, `/preview`, `POST /graph`, and `/stream`.
