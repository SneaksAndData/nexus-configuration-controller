FROM --platform=$BUILDPLATFORM golang:1.23-bookworm AS build-stage

ARG TARGETOS 
ARG TARGETARCH

WORKDIR /app

COPY . ./

RUN go mod verify && go mod vendor

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /app -v ./... 

# Deploy the application binary into a lean image
FROM gcr.io/distroless/static-debian12 AS build-release-stage

WORKDIR /app

COPY --from=build-stage /app /app

USER nonroot:nonroot

ENTRYPOINT ["/app/nexus-configuration-controller"]
