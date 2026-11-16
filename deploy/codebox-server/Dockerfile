# Build the stateless codebox-server binary from the Go workspace.
FROM golang:1.26.10 AS build
WORKDIR /src
COPY packages/secrets/ packages/secrets/
COPY apps/codebox-server/ apps/codebox-server/
WORKDIR /src/apps/codebox-server
RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /out/codebox-server ./cmd/codebox-server

# Static non-root runtime. The binary listens on PORT (8080 by default).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/codebox-server /usr/local/bin/codebox-server
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/codebox-server"]
