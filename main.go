package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/nlopes/slack"
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
	awsLbInternal = "service.beta.kubernetes.io/aws-load-balancer-internal"
	awsLbInternalValue = "0.0.0.0/0"
	gcpLbInternal = "cloud.google.com/load-balancer-type"
	gcpLbInternalValue = "internal"
)

var (
	kubeconfig = flag.String("kubeconfig", "", "Absolute path to the kubeconfig file, otherwise assume running in-cluster.")
	listenAddr = flag.String("listen-address", ":8080", "Address to listen on for HTTP requests.")
	terminate = flag.Bool("terminate", false, "Terminate public services immediately.")
	slackToken = flag.String("slack-token", "", "Slack API token to send notifications.")
	slackChan = flag.String("slack-channel", "", "Slack channel to notify when terminating services.")
	provider = flag.String("provider", "aws", "Cloud provider that is being used (aws or gcp)")
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
	if *provider == "aws" {
		return svc.Spec.Type != v1.ServiceTypeLoadBalancer ||
			svc.Annotations[awsLbInternal] == awsLbInternalValue
	} else {
		return svc.Spec.Type != v1.ServiceTypeLoadBalancer ||
			svc.Annotations[gcpLbInternal] == gcpLbInternalValue
	}
}

func terminator(client kubernetes.Interface, notify func(svc *v1.Service)) {
	fifo := cache.NewFIFO(cache.MetaNamespaceKeyFunc)
	cache.NewReflector(
		cache.NewListWatchFromClient(client.Core().GetRESTClient(), "services", api.NamespaceAll, nil),
		&v1.Service{},
		fifo,
		0,
	).Run()

	for {
		item, err := fifo.Pop(func(item interface{}) error {
			svc := item.(*v1.Service)
			if isInternal(svc) {
				return nil
			}

			// Delete doesn't support a ResourceVersion
			// check for some reason, so it is
			// theoretically possible for someone to
			// modify the Service to use an internal LB,
			// and *then* for our Delete to kill them.
			// The UID check at least makes sure we don't
			// kill the wrong incarnation of a Service
			// across delete-recreate.
			opts := api.DeleteOptions {
				Preconditions: &api.Preconditions{
					UID: &svc.UID,
				},
			}
			err := client.Core().Services(svc.Namespace).Delete(svc.Name, &opts)
			if err != nil {
				return cache.ErrRequeue{err}
			}
			return nil
		})

		svc := item.(*v1.Service)
		if !isInternal(svc) {
			if err != nil {
				log.Printf("Error deleting %s/%s: %s\n", svc.Namespace, svc.Name, err)
				continue
			}
			log.Printf("Deleted external service %s/%s\n", svc.Namespace, svc.Name)
			notify(svc)
		}
	}
}

func notifySlack(svc *v1.Service) {
	if *slackToken == "" {
		return
	}

	slackApi := slack.New(*slackToken)
	msg := fmt.Sprintf("Cool story bro: kube-svc-watch just deleted a public Service (%s/%s)! kthxbye.", svc.Namespace, svc.Name)
	chanId, timestamp, err := slackApi.PostMessage(*slackChan, msg, slack.PostMessageParameters{})
	if err != nil {
		log.Printf("Error posting to slack %s: %s\n", *slackChan, err)
		return
	}
	log.Printf("Sent notification to slack %s (%s) at %s\n", *slackChan, chanId, timestamp)
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

	if *terminate {
		log.Printf("Termination mode engaged\n")
		go terminator(clientset, notifySlack)
	}

	if *provider == "aws" {
		log.Printf("Using AWS provider\n")
	} else if *provider == "gcp" {
		log.Printf("Using GCP provider\n")
	} else {
		panic("unknown provider specified")
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
