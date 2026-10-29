# CLI graph examples

The examples in this directory show the complete declarative shape used by `stream-demo`:

```bash
SourceInput -> ContainerParse -> Output
```

Files:

- `source-parse-output.json`
- `source-parse-output.yaml`

Validate either file before applying it:

```bash
go run ./cmd/stream-demo check -graph ./examples/source-parse-output.yaml
go run ./cmd/stream-demo check -graph ./examples/source-parse-output.json
```

`check` runs the same graph apply validation used by `MediaGraph`, then reports warnings such as a missing source, missing `ContainerParse`, or unknown link endpoints. Warnings describe showcase limitations; graph construction errors still fail the command.
