package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	apicorev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	informercorev1 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	listercorev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// AController does this
type AController struct {
	secretGetter          corev1.SecretsGetter
	secretLister          listercorev1.SecretLister
	secretListerSynced    cache.InformerSynced
	namespaceGetter       corev1.NamespacesGetter
	namespaceLister       listercorev1.NamespaceLister
	namespaceListerSynced cache.InformerSynced

	queue workqueue.RateLimitingInterface
}

const (
	secretSyncKey          = "do it"
	secretSyncSrcNamespace = "secretsyncns"
	secretSyncAnnotation   = "com.company/name"
)

// we will be pasting the secret to the all the namespaces that have secretSyncAnnotation
// var namespaceBlackList = map[string]bool{
// 	"kube-system":          true,
// 	"kube-public":          true,
// 	secretSyncSrcNamespace: true,
// }

// NewController foes this
func NewController(client *kubernetes.Clientset, secretInfromer informercorev1.SecretInformer, namespaceInformer informercorev1.NamespaceInformer) *AController {
	c := &AController{
		secretGetter:          client.CoreV1(),
		secretLister:          secretInfromer.Lister(),
		secretListerSynced:    secretInfromer.Informer().HasSynced,
		namespaceGetter:       client.CoreV1(),
		namespaceLister:       namespaceInformer.Lister(),
		namespaceListerSynced: namespaceInformer.Informer().HasSynced,

		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "secretsync"),
	}

	secretInfromer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				log.Print("Secret Added")
				c.ScheduleSecretSync()
			},
			UpdateFunc: func(oldObj, obj interface{}) {
				log.Print("Secret updated")
				c.ScheduleSecretSync()
			},
			DeleteFunc: func(obj interface{}) {
				log.Print("Secret deleted")
				c.ScheduleSecretSync()
			},
		},
	)

	namespaceInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				log.Print("namespace added")
				c.ScheduleSecretSync()
			},
			UpdateFunc: func(oldObj, obj interface{}) {
				log.Print("Namespace updated")
				c.ScheduleSecretSync()
			},
			DeleteFunc: func(obj interface{}) {
				log.Print("Nameapce delete")
				c.ScheduleSecretSync()
			},
		},
	)
	return c
}

// Run does this
func (c *AController) Run(stop <-chan struct{}) {
	var wg sync.WaitGroup
	defer func() {
		log.Print("Shutting down queue")
		c.queue.ShutDown()

		log.Print("Shutting down workeres")
		wg.Wait()

		log.Print("Workers are all done")
	}()

	log.Print("Waiting for cache sync")
	if !cache.WaitForCacheSync(
		stop,
		c.secretListerSynced,
		c.namespaceListerSynced) {
		log.Print("Timed out waiting for cache sync")
		return
	}

	log.Print("Caches are synced")

	go func() {
		wait.Until(c.runWorker, time.Second, stop)

		wg.Done()
	}()

	log.Print("Waiting for stop signal")
	<-stop
	log.Print("received stop signal")
}

//ScheduleSecretSync does this
func (c *AController) ScheduleSecretSync() {

	c.queue.Add(secretSyncKey)
}

func (c *AController) runWorker() {
	for c.processNextWorkItem() {

	}
}

func (c *AController) processNextWorkItem() bool {
	log.Print("inside process next itme")
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)
	err := c.doSync()
	if err == nil {
		c.queue.Forget(key)
		return true
	}

	runtime.HandleError(fmt.Errorf("doSync failed with %v", err))
	c.queue.AddRateLimited(key)

	return true
}

func (c *AController) doSync() error {

	// srcSecrets are all the secrets from the namespace that we are listening to and
	// have the specified annotations
	srcSecrets, err := c.getSecretsInNS(secretSyncSrcNamespace)
	if err != nil {
		log.Printf("There was an error getting the secret from the namespaces")
	}

	// get all the namespaces and then we will get secrets from those namespaces
	rawNamesapces, err := c.namespaceLister.List(labels.Everything())
	if err != nil {
		log.Printf("There was an error getting all the namespaces")
	}

	// we dont have to create the secret in all the namespace
	// we have to target the namespaces except kube-system, kube-public, and the namespace to which we are listening to
	var targetNamespaces []*apicorev1.Namespace
	for _, ns := range rawNamesapces {
		if _, ok := ns.Annotations[secretSyncAnnotation]; ok {
			targetNamespaces = append(targetNamespaces, ns)
		}
	}

	for _, ns := range targetNamespaces {
		c.syncSecrets(srcSecrets, ns)
	}

	log.Printf("Finishing doSync")
	return err
}

func (c *AController) getSecretsInNS(ns string) ([]*apicorev1.Secret, error) {
	rawSecrets, err := c.secretLister.Secrets(ns).List(labels.Everything())
	if err != nil {
		return nil, err
	}

	var secrets []*apicorev1.Secret
	for _, secret := range rawSecrets {
		if _, ok := secret.Annotations[secretSyncAnnotation]; ok {
			secrets = append(secrets, secret)
		}
	}
	return secrets, nil
}

func (c *AController) syncSecrets(secrets []*apicorev1.Secret, ns *apicorev1.Namespace) {
	// 1. Create/Update all of the secrets in the namespace
	for _, secret := range secrets {
		log.Printf("The secret %v should be created in the namespace %v", secret, ns.GetName())
		for _, secret := range secrets {
			newSecret := secret.DeepCopy()
			newSecret.Namespace = ns.ObjectMeta.Namespace
			newSecret.ResourceVersion = ""
			newSecret.UID = ""

			log.Printf("Creating new secret %v in the namesapce %v", newSecret.Name, ns.ObjectMeta.Name)
			_, err := c.secretGetter.Secrets(ns.GetName()).Create(newSecret)

			if apierrors.IsAlreadyExists(err) {
				// I like providing the compolete field name to access struct keys
				log.Printf("Scratch that, updating secret %v in namespace %v", newSecret.ObjectMeta.Name, ns.ObjectMeta.Name)
				_, err = c.secretGetter.Secrets(ns.ObjectMeta.Name).Update(newSecret)
				if err != nil {
					log.Printf("There was an error updating the secret %v", err)
				}
			}
			if err != nil {
				log.Printf("there was an error creating the new secret %v", err)
			}

		}
	}

	// 2. Delete already created secrets that have the same annotation but are not present in out source namespace
	srcSecrets := sets.String{}
	targetSecrets := sets.String{}

	for _, secret := range secrets {
		srcSecrets.Insert(secret.Name)
	}

	targetSecretsList, err := c.getSecretsInNS(ns.ObjectMeta.Name)
	if err != nil {
		log.Printf("There was an error getting the target secrets from a namespace ")
	}
	for _, targettSecret := range targetSecretsList {
		targetSecrets.Insert(targettSecret.ObjectMeta.Name)
	}

	deleteSet := targetSecrets.Difference(srcSecrets)
	for secretToDel := range deleteSet {
		log.Printf("Deleting secret %v from namespace %v", secretToDel, ns.ObjectMeta.Name)
		err = c.secretGetter.Secrets(ns.ObjectMeta.Name).Delete(secretToDel, nil)
		if err != nil {
			log.Printf("There was an error deleting the secret %v from the namespace %v and error is %v", secretToDel, ns.ObjectMeta.Name, err)
		}
	}

}
