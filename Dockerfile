FROM golang:1.14.6-alpine3.12 AS builder

RUN mkdir /app 
ADD . /app/ 
WORKDIR /app
RUN CGO_ENABLED=0 GOOS=linux go build -mod vendor -a -installsuffix cgo -o server .

FROM alpine:3.11
ARG GO_TELEGRAM_BOT_BRANCH
ENV GO_TELEGRAM_BOT_BRANCH $GO_TELEGRAM_BOT_BRANCH

WORKDIR /

COPY --from=builder /app/server /server
ENTRYPOINT ["/server"]
