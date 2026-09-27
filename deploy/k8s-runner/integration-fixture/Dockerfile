FROM golang:1.25 AS build
WORKDIR /src
COPY deploy/k8s-runner/integration-fixture/main.go .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /fixture main.go

FROM scratch
COPY --from=build /fixture /fixture
USER 10001:10001
ENTRYPOINT ["/fixture"]
