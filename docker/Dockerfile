FROM alpine:3.5

RUN apk --update add ca-certificates && rm -rf /var/cache/apk/*

COPY kube-svc-watch /usr/local/bin/

EXPOSE 8080
CMD ["/usr/local/bin/kube-svc-watch", "--logtostderr"]
