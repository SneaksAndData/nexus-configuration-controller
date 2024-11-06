FROM --platform=$BUILDPLATFORM golang:1.23-bookworm AS build-stage

ARG TARGETOS 
ARG TARGETARCH

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download && go get ./...

COPY *.go ./

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /ncc

# Deploy the application binary into a lean image
FROM gcr.io/distroless/static-debian12 AS build-release-stage

WORKDIR /

COPY --from=build-stage /ncc /ncc

USER nonroot:nonroot

ENTRYPOINT ["/ncc"]
