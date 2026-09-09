# Multi-stage build for both prreview binaries; select with
#   docker build --target receiver  /  --target worker
# from the repository root:
#   docker build -f services/prreview/Dockerfile --target worker .

FROM golang:1.25 AS build
WORKDIR /src
COPY . .
WORKDIR /src/services/prreview
RUN CGO_ENABLED=0 go build -o /out/receiver ./cmd/receiver \
    && CGO_ENABLED=0 go build -o /out/worker ./cmd/worker

# The receiver is pure network + Firestore + Tasks: distroless.
FROM gcr.io/distroless/static-debian12:nonroot AS receiver
COPY --from=build /out/receiver /receiver
ENTRYPOINT ["/receiver"]

# The worker shells out to git for the checkout, and the review engine
# runs a tenant repository's *declared* checks with the tools it detects:
# shipping go + make enables the declared gates; a tool absent from the
# image makes its checks report "unavailable" rather than fail (the
# engine's existing contract), so the image's tool set only widens or
# narrows coverage, never correctness.
FROM golang:1.25 AS worker
RUN apt-get update && apt-get install -y --no-install-recommends git make ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/worker /worker
USER 65532:65532
ENTRYPOINT ["/worker"]
