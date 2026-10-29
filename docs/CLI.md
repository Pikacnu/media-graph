# CLI Tutorial

## Commands

```bash
go run ./cmd/stream-demo help
go run ./cmd/stream-demo inspect
go run ./cmd/stream-demo check -graph ./examples/source-parse-output.yaml
go run ./cmd/stream-demo apply -graph ./examples/source-parse-output.json
go run ./cmd/stream-demo run -graph ./graph.yaml -input ./frame.raw
go run ./cmd/stream-demo serve -graph ./graph.yaml
```

- `check` validates a graph and reports warnings.
- `apply` creates a graph, applies it once, prints the version, and exits.
- `run` applies a graph, sends input, receives output, and exits.
- `serve` keeps one graph alive until Ctrl+C.
- `preview` reads a running graph snapshot.
- `inspect` lists registered node factories and pad arity.

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

A deterministic browser demo can use a `TestSource → Output` graph with raw RGB caps.

## Docker

```bash
docker build -t stream-things-demo:local .
docker run --rm -p 8091:8091 stream-things-demo:local
```

Then use `/healthz`, `/preview`, `/graph`, or `/stream` on port 8091.
