FROM golang:1.26-bookworm AS build

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       libgstreamer1.0-dev \
       libgstreamer-plugins-base1.0-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /out/stream-demo ./cmd/stream-demo

FROM ubuntu:rolling

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       gstreamer1.0-tools \
       gstreamer1.0-plugins-base \
       gstreamer1.0-plugins-good \
       gstreamer1.0-plugins-bad \
       gstreamer1.0-plugins-ugly \
       gstreamer1.0-libav \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/stream-demo /usr/local/bin/stream-demo
COPY examples /opt/stream-things-demo/examples

WORKDIR /opt/stream-things-demo
EXPOSE 8091
ENTRYPOINT ["stream-demo"]
CMD ["serve", "-graph", "/opt/stream-things-demo/examples/source-parse-output.yaml", "-listen", "0.0.0.0:8091"]
