FROM busybox:1.26.2-uclibc

COPY kube-svc-watch /usr/local/bin/

EXPOSE 8080
CMD ["/usr/local/bin/kube-svc-watch", "--logtostderr"]
