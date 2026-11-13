FROM golang:1.25.13-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/mock-llm ./cmd/mock-llm
RUN CGO_ENABLED=0 GOOS=linux go build -o /mock-llm ./cmd/mock-llm

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /mock-llm /mock-llm
EXPOSE 8091
ENTRYPOINT ["/mock-llm"]
