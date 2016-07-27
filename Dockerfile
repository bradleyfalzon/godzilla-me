FROM golang:1.6-onbuild

RUN go get github.com/hydroflame/godzilla
# VOLUME /go/src/app/results.db
EXPOSE 80
WORKDIR /go/src/app
CMD app
