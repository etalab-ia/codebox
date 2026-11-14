# Build the stateless secrets-gateway binary from the Go workspace.
FROM golang:1.26.10 AS build
WORKDIR /src
COPY packages/secrets/ packages/secrets/
COPY apps/secrets-gateway/ apps/secrets-gateway/
WORKDIR /src/apps/secrets-gateway
RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /out/secrets-gateway ./cmd/secrets-gateway

# Static non-root runtime. The binary listens on PORT (8080 by default).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/secrets-gateway /usr/local/bin/secrets-gateway
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/secrets-gateway"]
