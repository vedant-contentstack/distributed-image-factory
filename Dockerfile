FROM golang:1.24.6 as build
WORKDIR /app
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o server ./cmd/server

FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=build /app/server /app/server
COPY --from=build /app/web /app/web
ENV GRID_BIND=0.0.0.0:9100
EXPOSE 8080 9100
CMD ["/app/server"]


