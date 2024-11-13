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

COPY --from=public.ecr.aws/aws-cli/aws-cli /usr/local/aws-cli/ /usr/local/aws-cli/
COPY --from=public.ecr.aws/aws-cli/aws-cli /usr/local/bin/aws /usr/local/bin/aws 

USER nonroot:nonroot

ENTRYPOINT ["/app/nexus-configuration-controller"]
