FROM golang:1.25.12-alpine@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587 AS build
ARG VERSION=devel
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/adamraziv/dataporch/internal/app.releaseVersion=$VERSION" -o /out/dataporch ./cmd/dataporch

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/dataporch /usr/local/bin/dataporch
USER nonroot
ENTRYPOINT ["/usr/local/bin/dataporch"]
CMD ["run", "-f"]
