FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/examd ./cmd/examd
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/exam-hook ./cmd/exam-hook

FROM alpine:3.20

RUN apk add --no-cache ca-certificates docker-cli tzdata
WORKDIR /app
COPY --from=build /out/examd /app/examd
COPY --from=build /out/exam-hook /app/exam-hook
COPY deploy/config-example.yaml /app/config-example.yaml

EXPOSE 8080
ENTRYPOINT ["/app/examd"]
CMD ["serve", "-config", "/app/config.yaml"]
