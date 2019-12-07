FROM golang:1.13.5 as builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./
COPY pkg ./pkg
RUN go build -o /kube-plex .

FROM alpine:3.6

COPY --from=builder /kube-plex /kube-plex
