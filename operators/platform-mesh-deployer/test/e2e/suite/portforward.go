/*
Copyright The Platform Mesh Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package suite

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Ports and pod selectors of the services the host has to reach.
var (
	// frontProxy serves on 6443; see kcp-operator
	// internal/resources/frontproxy/service.go.
	frontProxy = forwardTarget{
		what:      "front proxy",
		namespace: ProviderNamespace,
		port:      6443,
		labels: ctrlruntimeclient.MatchingLabels{
			"app.kubernetes.io/managed-by": "kcp-operator",
			"app.kubernetes.io/component":  "front-proxy",
		},
	}
	// registry is the OCM registry from config/bases/registry.
	registry = forwardTarget{
		what:      "registry",
		namespace: ProviderNamespace,
		port:      5000,
		labels:    ctrlruntimeclient.MatchingLabels{"app": "registry"},
	}
)

// forwardTarget is a pod to forward a local port to.
type forwardTarget struct {
	what      string
	namespace string
	port      int
	labels    ctrlruntimeclient.MatchingLabels
}

// FrontProxyDialer dials the front proxy running on c through a port-forward.
//
// The front proxy is only reachable on the kind node's address, which lives on
// the container runtime's network and is not routable from the host on every
// runtime. Forwarding through the API server works wherever the kubeconfig
// does. The forward is established on first use, because the deployer creates
// the front proxy well after the dialer is handed out, and reused after that.
func FrontProxyDialer(t *testing.T, c *Cluster) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()
	return lazyDialer(t, c, frontProxy)
}

// lazyDialer dials target on c through a port-forward set up on first use.
//
// A forward pins one pod, and the deployer rolls the front proxy whenever a
// module publishes a path mapping, so the pinned pod is deleted mid-test. A
// failed dial therefore re-establishes the forward instead of failing for the
// rest of the test.
func lazyDialer(t *testing.T, c *Cluster, target forwardTarget) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()
	var (
		mu   sync.Mutex
		addr string
	)
	take := func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if addr == "" {
			forwarded, err := forward(t, c, target)
			if err != nil {
				return "", err
			}
			addr = forwarded
		}
		return addr, nil
	}
	drop := func(stale string) {
		mu.Lock()
		defer mu.Unlock()
		if addr == stale {
			addr = ""
		}
	}

	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		var err error
		for range 2 {
			var local string
			if local, err = take(); err != nil {
				return nil, err
			}
			var conn net.Conn
			if conn, err = (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", local); err == nil {
				return conn, nil
			}
			drop(local)
			if ctx.Err() != nil {
				break
			}
		}
		return nil, err
	}
}

// forward forwards an ephemeral local port to a pod matching target on c and
// returns the local address. The forward runs until the test ends.
func forward(t *testing.T, c *Cluster, target forwardTarget) (string, error) {
	t.Helper()

	pods := &corev1.PodList{}
	if err := c.Client.List(t.Context(), pods,
		ctrlruntimeclient.InNamespace(target.namespace), target.labels); err != nil {
		return "", fmt.Errorf("listing %s pods: %w", target.what, err)
	}
	var pod string
	for i := range pods.Items {
		if isPodReady(&pods.Items[i]) {
			pod = pods.Items[i].Name
			break
		}
	}
	if pod == "" {
		return "", fmt.Errorf("no ready %s pod on %s", target.what, c.Name)
	}

	clientset, err := kubernetes.NewForConfig(c.Config)
	if err != nil {
		return "", err
	}
	transport, upgrader, err := spdy.RoundTripperFor(c.Config)
	if err != nil {
		return "", err
	}
	url := clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(target.namespace).Name(pod).SubResource("portforward").URL()

	stop, ready := make(chan struct{}), make(chan struct{})
	fw, err := portforward.NewOnAddresses(
		spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, url),
		[]string{"127.0.0.1"}, []string{fmt.Sprintf("0:%d", target.port)},
		stop, ready, nil, nil,
	)
	if err != nil {
		return "", err
	}

	errs := make(chan error, 1)
	go func() { errs <- fw.ForwardPorts() }()

	select {
	case <-ready:
	case err := <-errs:
		return "", fmt.Errorf("forwarding to %s: %w", pod, err)
	case <-time.After(30 * time.Second):
		close(stop)
		return "", fmt.Errorf("timed out forwarding to %s", pod)
	}
	t.Cleanup(func() { close(stop) })

	ports, err := fw.GetPorts()
	if err != nil {
		return "", err
	}
	local := net.JoinHostPort("127.0.0.1", fmt.Sprint(ports[0].Local))
	t.Logf("%s on %s forwarded to %s via pod %s", target.what, c.Name, local, pod)
	return local, nil
}

func isPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}
