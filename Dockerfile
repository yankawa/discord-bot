# syntax=docker/dockerfile:1
FROM golang:1.27.4-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG BOT=times-bot
ARG VERSION=0.1.0
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/bot "./cmd/${BOT}"
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian13:nonroot
WORKDIR /app
COPY --from=build /out/bot /bot
COPY --from=build --chown=65532:65532 /out/data /app/data
USER 65532:65532
ENTRYPOINT ["/bot"]
