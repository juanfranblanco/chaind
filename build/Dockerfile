# this dockerfile cross-compiles chaind for linux operating systems.
FROM golang:1.11.1-alpine3.8

RUN apk add git make curl && \
    curl -L -s https://raw.githubusercontent.com/golang/dep/v0.5.0/install.sh | sh

COPY workspace $GOPATH/src/github.com/kyokan/chaind
COPY build-cross.sh /

CMD ["/build-cross.sh"]