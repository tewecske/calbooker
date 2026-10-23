# syntax=docker/dockerfile:1
#
# The calbooker image. Templates, static assets, translations, and migrations
# are embedded in the binary, so the final image holds only the binary and CA
# certificates. See deploy/ and docs/deployment.md.

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/calbooker ./cmd/web

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/calbooker /calbooker
USER nonroot:nonroot
ENTRYPOINT ["/calbooker"]
