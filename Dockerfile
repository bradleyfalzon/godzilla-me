FROM golang:1.6-onbuild
RUN go get github.com/hydroflame/godzilla/cmd/godzilla
EXPOSE 80
WORKDIR /go/src/app
CMD app
