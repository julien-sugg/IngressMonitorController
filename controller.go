package main

import (
	"fmt"
	"time"

	"github.com/golang/glog"
	"k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// MonitorController which can be used for monitoring ingresses
type MonitorController struct {
	clientset *kubernetes.Clientset
	namespace string
	indexer   cache.Indexer
	queue     workqueue.RateLimitingInterface
	informer  cache.Controller
}

//
func NewMonitorController(namespace string, clientset *kubernetes.Clientset) *MonitorController {
	controller := &MonitorController{
		clientset: clientset,
		namespace: namespace,
	}

	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	// Create the Ingress Watcher
	ingressListWatcher := cache.NewListWatchFromClient(clientset.ExtensionsV1beta1().RESTClient(), "ingresses", namespace, fields.Everything())

	indexer, informer := cache.NewIndexerInformer(ingressListWatcher, &v1beta1.Ingress{}, 0, cache.ResourceEventHandlerFuncs{
		AddFunc:    controller.onIngressAdded,
		UpdateFunc: controller.onIngressUpdated,
		DeleteFunc: controller.onIngressDeleted,
	}, cache.Indexers{})
	controller.indexer = indexer
	controller.informer = informer
	controller.queue = queue

	return controller
}

func (c *MonitorController) Run(threadiness int, stopCh chan struct{}) {
	defer runtime.HandleCrash()

	// Let the workers stop when we are done
	defer c.queue.ShutDown()
	glog.Info("Starting Ingress Monitor controller")

	go c.informer.Run(stopCh)

	// Wait for all involved caches to be synced, before processing items from the queue is started
	if !cache.WaitForCacheSync(stopCh, c.informer.HasSynced) {
		runtime.HandleError(fmt.Errorf("Timed out waiting for caches to sync"))
		return
	}

	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	<-stopCh
	glog.Info("Stopping Ingress Monitor controller")
}

func (c *MonitorController) runWorker() {
	for c.processNextItem() {
	}
}

func (c *MonitorController) processNextItem() bool {
	// Wait until there is a new item in the working queue
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	// Tell the queue that we are done with processing this key. This unblocks the key for other workers
	// This allows safe parallel processing because two ingresses with the same key are never processed in
	// parallel.
	defer c.queue.Done(key)

	// Invoke the method containing the business logic
	err := c.syncToStdout(key.(string))
	// Handle the error if something went wrong during the execution of the business logic
	c.handleErr(err, key)
	return true
}

// syncToStdout is the business logic of the controller. In this controller it simply prints
// information about the ingress to stdout. In case an error happened, it has to simply return the error.
// The retry logic should not be part of the business logic.
func (c *MonitorController) syncToStdout(key string) error {
	obj, exists, err := c.indexer.GetByKey(key)
	if err != nil {
		glog.Errorf("Fetching object with key %s from store failed with %v", key, err)
		return err
	}

	if !exists {
		// Below we will warm up our cache with an Ingress, so that we will see a delete for one ingress
		fmt.Printf("Ingress %s does not exist anymore\n", key)
	} else {
		ingress := obj.(*v1beta1.Ingress)
		fmt.Println(" Something Changed in ingress: " + ingress.GetName())
		// Note that you also have to check the uid if you have a local controlled resource, which
		// is dependent on the actual instance, to detect that an Ingress was recreated with the same name
	}
	return nil
}

// handleErr checks if an error happened and makes sure we will retry later.
func (c *MonitorController) handleErr(err error, key interface{}) {
	if err == nil {
		// Forget about the #AddRateLimited history of the key on every successful synchronization.
		// This ensures that future processing of updates for this key is not delayed because of
		// an outdated error history.
		c.queue.Forget(key)
		return
	}

	// This controller retries 5 times if something goes wrong. After that, it stops trying.
	if c.queue.NumRequeues(key) < 5 {
		glog.Infof("Error syncing ingress %v: %v", key, err)

		// Re-enqueue the key rate limited. Based on the rate limiter on the
		// queue and the re-enqueue history, the key will be processed later again.
		c.queue.AddRateLimited(key)
		return
	}

	c.queue.Forget(key)
	// Report to an external entity that, even after several retries, we could not successfully process this key
	runtime.HandleError(err)
	glog.Infof("Dropping ingress %q out of the queue: %v", key, err)
}

func (c *MonitorController) onIngressAdded(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err == nil {
		c.queue.Add(key)
	}
}

func (c *MonitorController) onIngressUpdated(old interface{}, new interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(new)
	if err == nil {
		c.queue.Add(key)
	}
}

func (c *MonitorController) onIngressDeleted(obj interface{}) {
	// IndexerInformer uses a delta queue, therefore for deletes we have to use this
	// key function.
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err == nil {
		fmt.Println("There is no error in deletion")
		c.queue.Add(key)
	} else {
		fmt.Println("Error: " + err.Error())
	}
}
