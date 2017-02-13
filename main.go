package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/pkg/api"
	"k8s.io/client-go/1.5/pkg/api/v1"
	"k8s.io/client-go/1.5/rest"
	"k8s.io/client-go/1.5/tools/cache"
	"k8s.io/client-go/1.5/tools/clientcmd"
)

const (
	lbInternal = "service.beta.kubernetes.io/aws-load-balancer-internal"
	lbInternalValue = "0.0.0.0/0"
)

var (
	kubeconfig = flag.String("kubeconfig", "", "Absolute path to the kubeconfig file, otherwise assume running in-cluster.")
	listenAddr = flag.String("listen-address", ":8080", "Address to listen on for HTTP requests.")
)

var (
	svcInfo = prometheus.NewDesc(
		"kube_service_info",
		"Information about cluster services.",
		[]string{
			"kubernetes_namespace",
			"kubernetes_name",
			"type",
			"internal",
		}, nil,
	)
)

type svcCollector struct {
	store cache.Store
}

func (c svcCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- svcInfo
}

func (c svcCollector) collectSvc(ch chan<- prometheus.Metric, svc *v1.Service) {
	ch <- prometheus.MustNewConstMetric(svcInfo,
		prometheus.GaugeValue, 1,
		// Order must match svcInfo!
		svc.Namespace,
		svc.Name,
		string(svc.Spec.Type),
		fmt.Sprintf("%v", isInternal(svc)),
	)
}

func (c svcCollector) Collect(ch chan<- prometheus.Metric) {
	for _, item := range c.store.List() {
		c.collectSvc(ch, item.(*v1.Service))
	}
}

func isInternal(svc *v1.Service) bool {
	return svc.Spec.Type != v1.ServiceTypeLoadBalancer ||
		svc.Annotations[lbInternal] == lbInternalValue
}

func main() {
	flag.Parse()

	var config *rest.Config
	var err error
	if *kubeconfig == "" {
		config, err = rest.InClusterConfig()
	} else {
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	}
	if err != nil {
		panic(err.Error())
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	store := cache.NewStore(cache.MetaNamespaceKeyFunc)

	cache.NewReflector(
		cache.NewListWatchFromClient(clientset.Core().GetRESTClient(), "services", api.NamespaceAll, nil),
		&v1.Service{},
		store,
		0,
	).Run()

	prometheus.MustRegister(svcCollector{store})

	http.Handle("/metrics", promhttp.Handler())

	log.Printf("Serving on %v\n", *listenAddr)
	log.Fatal(http.ListenAndServe(*listenAddr, nil))
}
