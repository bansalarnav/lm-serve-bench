FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN go build -o /out/lm-serve-bench ./cmd/lm-serve-bench

FROM python:3.12-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl build-essential \
    && rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir aiperf

WORKDIR /bench
COPY --from=build /out/lm-serve-bench /usr/local/bin/lm-serve-bench
COPY data ./data
COPY docs ./docs
COPY README.md ./README.md

ENTRYPOINT ["lm-serve-bench", "run"]
