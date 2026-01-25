FROM golang:1.25.6-alpine AS builder

WORKDIR /app

COPY go.mod ./
RUN go mod download && go mod verify

COPY . .

RUN go build -o main main.go

FROM alpine:3.21.1

COPY --from=builder /app/main /app/main

CMD ["/app/main"]