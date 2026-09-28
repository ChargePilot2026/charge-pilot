ARG SERVICE=central
FROM golang:1.27 AS build
ARG SERVICE
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/service ./cmd/${SERVICE}

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/service /app/service
COPY migrations /app/migrations
USER nonroot:nonroot
ENTRYPOINT ["/app/service"]
