FROM golang:1.25.13-bookworm AS builder

LABEL authors="moyu114"

WORKDIR /app

COPY go.* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o unichatgo && CGO_ENABLED=0 GOOS=linux go build -o cloud-init ./cmd/cloud-init

# Use debian 12 slim
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
# RDS uses its own CA chain. Keep certificate verification enabled for tls=true.
ADD https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem /tmp/aws-rds-ca.pem
RUN cat /tmp/aws-rds-ca.pem >> /etc/ssl/certs/ca-certificates.crt && rm /tmp/aws-rds-ca.pem

WORKDIR /app
COPY --from=builder /app/unichatgo .
COPY --from=builder /app/cloud-init .
# For China mainland config.json.example_cn
# For Global config.json.example
COPY config.json.example ./config.json
RUN mkdir -p /app/data
RUN mkdir -p /app/data/mysql
RUN mkdir -p /app/data/redis
RUN useradd --system --uid 10001 --home-dir /app appuser && chown -R appuser:appuser /app/data

ENV UNICHATGO_CONFIG=/app/config.json
ENV UNICHATGO_DB=mysql
USER 10001:10001
EXPOSE 8090
CMD ["./unichatgo"]
