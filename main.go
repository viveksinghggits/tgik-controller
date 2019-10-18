package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	log.Printf("Very basic controller")
	kubeconfig := ""
	flag.StringVar(&kubeconfig, "kubeconfig", kubeconfig, "kubeconfig file")
	flag.Parse()

	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	var (
		config *rest.Config
		err    error
	)

	if kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		config, err = rest.InClusterConfig()
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating client: %v", err)
		os.Exit(1)
	}
	client := kubernetes.NewForConfigOrDie(config)

	sharedInformers := informers.NewSharedInformerFactory(client, 10*time.Minute)

	controller := NewController(client, sharedInformers.Core().V1().Secrets(), sharedInformers.Core().V1().Namespaces())

	sharedInformers.Start(nil)
	controller.Run(nil)
}
