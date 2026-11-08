# CLI Tutorial

## Commands

```bash
go run ./cmd/stream-demo help
go run ./cmd/stream-demo inspect
go run ./cmd/stream-demo check -graph ./examples/source-parse-output.yaml
go run ./cmd/stream-demo apply -graph ./examples/source-parse-output.json
go run ./cmd/stream-demo run -graph ./graph.yaml -input ./frame.raw
go run ./cmd/stream-demo serve -graph ./graph.yaml
go run ./cmd/stream-demo version
```

- `check` validates a graph and reports warnings.
- `apply` creates a graph, applies it once, prints the version, and exits.
- `run` applies a graph, sends input, receives output, and exits.
- `serve` keeps one graph alive until Ctrl+C.
- `preview` reads a running graph snapshot.
- `inspect` lists registered node factories and pad arity.
- `version` prints the build tag, commit, and GStreamer runtime version.

Use `<command> -h` or `<command> --help` to print help and exit successfully.

### Input/output node selection for `run`

The built-in byte-input demo defaults to `-input-node demo_input -output-node demo_output`.
When using `-graph`, select the graph's SourceInput and Output node IDs explicitly if they
have different names:

```bash
go run ./cmd/stream-demo run -graph ./graph.yaml -input ./frame.raw -input-node source -output-node output
```

The input must match the graph's media format. `run` requires a byte-input SourceInput
and a byte-output Output node; it is not a runner for every graph type. For the autonomous
TestSource graph, use `serve` instead. The source/parse example is a graph-structure example,
not a general-purpose raw-frame decoder.

Release binaries are built with `-ldflags` so `version` reports the actual tag. Build locally
with the same flags to reproduce it:

```bash
go build -ldflags "-X main.version=v0.1.0-demo -X main.commit=$(git rev-parse --short HEAD) -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o ./dist/stream-demo ./cmd/stream-demo
./dist/stream-demo version
```

## Live snapshot and update

Start a graph:

```bash
export STREAM_DEMO_ENDPOINT="http://127.0.0.1:8091"
export STREAM_DEMO_LISTEN="127.0.0.1:8091"
go run ./cmd/stream-demo serve -graph ./examples/source-parse-output.yaml
```

Inspect it:

```bash
curl http://127.0.0.1:8091/healthz
curl http://127.0.0.1:8091/preview
go run ./cmd/stream-demo preview
```

Apply an updated graph to the running process:

```bash
go run ./cmd/stream-demo apply -graph ./examples/source-parse-output.yaml
```

The running process exposes:

```bash
GET  /healthz  current graph ID and version
GET  /preview  current graph snapshot as JSON
POST /graph    apply JSON or YAML to the running graph
GET  /stream   MJPEG preview for compatible raw RGB output
```

## Graph format

JSON and YAML describe the same `DesiredGraph`:

```bash
id
nodes
links
properties (optional)
```

Each node contains `id`, `node_type`, `properties`, `source_pad_ids`, and `sink_pad_ids`. Node map keys must match node IDs. Each link must reference declared source and sink pad IDs.

Use `stream-demo inspect` to find registered node types. Use `stream-demo check` before applying a graph.

The included examples describe:

```bash
SourceInput → ContainerParse → Output
```

## Self-running browser preview

The included [test-source-preview.yaml](../examples/test-source-preview.yaml) graph uses
`TestSource → Output` to generate a moving ball at 640×360, 30 fps, with raw RGB output.
It does not need an input file or an external producer.

Stop any existing server on port 8091, then run from the project root:

```powershell
go run .\cmd\stream-demo serve -graph .\examples\test-source-preview.yaml -listen 127.0.0.1:8091 -output-node demo_output -width 640 -height 360
```

In another PowerShell terminal:

```powershell
Start-Process "http://127.0.0.1:8091/stream"
go run .\cmd\stream-demo preview -running http://127.0.0.1:8091
```

Keep the server running while viewing the stream; Ctrl+C stops it, including active
browser streams. HTTP shutdown allows up to three seconds before forcing connections
closed; a second Ctrl+C restores the default interrupt behavior. The `-output-node`
value must match the graph's Output node ID, and `-width`/`-height` must match its raw RGB
caps. This example uses a width whose RGB rows have no GStreamer alignment padding.

`/preview` and the CLI `preview` command show graph information, not video. Opening
`/graph` in a browser returns HTTP 405 because that endpoint accepts POST only.
The source/parse examples wait for external input; they do not generate preview frames.

With the locally built Docker image:

```powershell
docker run --rm -p 8091:8091 stream-things-demo:local serve -graph examples/test-source-preview.yaml -listen 0.0.0.0:8091 -output-node demo_output -width 640 -height 360
```

The graph path in this command is inside the Linux container.

## Docker

```bash
docker build -t stream-things-demo:local .
docker run --rm stream-things-demo:local version
docker run --rm -p 8091:8091 stream-things-demo:local
```

The image is built with `VERSION`, `COMMIT`, and `BUILD_DATE` build arguments; the CI
passes the git tag and commit so `version` inside the container matches the release.

Then use `/healthz`, `/preview`, `/graph`, or `/stream` on port 8091.

## Release downloads

Release pages provide a Linux x86-64 executable and an OCI image archive. The executable
requires GStreamer 1.26 or newer on the host. The image includes its GStreamer runtime.

```bash
curl -LO https://github.com/Pikacnu/media-graph/releases/latest/download/stream-demo-linux-amd64
curl -LO https://github.com/Pikacnu/media-graph/releases/latest/download/stream-things-demo-image.tar.gz
curl -LO https://github.com/Pikacnu/media-graph/releases/latest/download/SHA256SUMS
sha256sum --check SHA256SUMS
chmod +x ./stream-demo-linux-amd64
```

```bash
docker load --input ./stream-things-demo-image.tar.gz
docker run --rm -p 8091:8091 stream-things-demo:release
```

The release image is an offline archive, not a registry publication. It can be moved
between Docker hosts without requiring registry credentials.
