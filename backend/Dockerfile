FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/agenttoolgate ./cmd/server

FROM alpine:3.21

RUN adduser -D -g '' appuser
WORKDIR /app
COPY --from=builder /out/agenttoolgate /usr/local/bin/agenttoolgate

EXPOSE 8080
USER appuser
ENTRYPOINT ["agenttoolgate"]
