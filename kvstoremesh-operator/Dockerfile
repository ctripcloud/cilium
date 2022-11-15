FROM golang:1.17.13

MAINTAINER arthurchiao@hotmail.com

WORKDIR /

ADD kvstoremesh-operator  /kvstoremesh-operator

RUN chmod +x /kvstoremesh-operator

ENTRYPOINT ["/kvstoremesh-operator"]
