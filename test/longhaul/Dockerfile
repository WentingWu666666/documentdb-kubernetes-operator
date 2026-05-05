# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Build stage
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum ./
RUN go mod download

# Copy source.
COPY . .

# Build the standalone binary.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /longhaul ./cmd/longhaul

# Runtime stage
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /longhaul /longhaul

USER nonroot:nonroot

ENTRYPOINT ["/longhaul"]
