# syntax=docker/dockerfile:1
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY internal/k8srunner ./internal/k8srunner
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /runner ./internal/k8srunner/cmd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /runner /runner
USER 10001:10001
WORKDIR /workspace
ENTRYPOINT ["/runner"]
