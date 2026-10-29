# Stream Things Graph Demo

An AI-assisted CLI demo of the graph-based GStreamer pipeline behind `stream-things`.

This is a **focused demo release**, not a complete streaming product. RTMP, SRT, WHIP,
frontend integration, and full audio mixing are out of scope.

## What it demonstrates

```bash
JSON/YAML graph
  -> node factory lookup
  -> MediaGraph.ApplyGraph
  -> GStreamer pipeline
  -> typed input/output handles
  -> live snapshot and graph update
  -> optional browser MJPEG preview
```

## Assets

| Asset | Purpose |
| --- | --- |
| `stream-demo-linux-amd64` | Linux x86-64 executable. Requires GStreamer 1.26+ on the host. |
| `stream-things-demo-image.tar` | Docker image archive with the GStreamer runtime included. |
| `SHA256SUMS` | Checksums for both assets. |

Verify the download:

```bash
sha256sum --check SHA256SUMS
```

## Run the executable

```bash
chmod +x ./stream-demo-linux-amd64
./stream-demo-linux-amd64 inspect
./stream-demo-linux-amd64 check -graph ./examples/source-parse-output.yaml
./stream-demo-linux-amd64 serve -graph ./examples/source-parse-output.yaml
```

## Run the container image

```bash
docker load --input ./stream-things-demo-image.tar
docker run --rm -p 8091:8091 stream-things-demo:release
```

Then probe the local control plane:

```bash
curl http://127.0.0.1:8091/healthz
curl http://127.0.0.1:8091/preview
curl -X POST --data-binary @./examples/source-parse-output.yaml http://127.0.0.1:8091/graph
```

Open `http://127.0.0.1:8091/stream` for the MJPEG preview.

## AI assistance disclosure

AI tools assisted with learning and applying the GStreamer and Go APIs, the CLI showcase and
demo workflows, graph architecture exploration, code and tests, documentation, debugging,
and the Docker and GitHub Actions setup. See the repository README for the full statement.

## Documentation

- `README.md` - overview and quick start
- `docs/CLI.md` - CLI, graph format, live snapshot/apply, and container tutorial