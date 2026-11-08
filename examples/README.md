# CLI graph examples

The examples in this directory show the complete declarative shape used by `stream-demo`:

```bash
SourceInput -> ContainerParse -> Output
```

Files:

- [source-parse-output.json](./source-parse-output.json): externally fed source/parse graph.
- [source-parse-output.yaml](./source-parse-output.yaml): the same graph in YAML.
- [test-source-preview.yaml](./test-source-preview.yaml): self-running `TestSource → Output`
  graph with a moving ball and 640×360 RGB output at 30 fps.

For a browser video preview, stop any existing server on port 8091 and run from the project root:

```powershell
go run .\cmd\stream-demo serve -graph .\examples\test-source-preview.yaml -listen 127.0.0.1:8091 -output-node demo_output -width 640 -height 360
```

Then open `http://127.0.0.1:8091/stream` in a browser. The CLI `preview` command and
`/preview` endpoint show graph information, not video. Press Ctrl+C to stop the server.

Validate either file before applying it:

```bash
go run ./cmd/stream-demo check -graph ./examples/source-parse-output.yaml
go run ./cmd/stream-demo check -graph ./examples/source-parse-output.json
```

`check` runs the same graph apply validation used by `MediaGraph`, then reports warnings such as a missing source, missing `ContainerParse`, or unknown link endpoints. Warnings describe showcase limitations; graph construction errors still fail the command.
