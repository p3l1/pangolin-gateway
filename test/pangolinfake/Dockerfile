# syntax=docker/dockerfile:1

# The fake Pangolin API for e2e. Never part of the production image: the
# controller's Dockerfile copies only cmd/ and internal/.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/ internal/
COPY test/ test/
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
        -ldflags "-s -w" -o /out/pangolin-fake ./test/pangolinfake/cmd

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /out/pangolin-fake /pangolin-fake
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/pangolin-fake"]
