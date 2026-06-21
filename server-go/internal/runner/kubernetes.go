package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/kubernetes"
	batchtypedv1 "k8s.io/client-go/kubernetes/typed/batch/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type KubernetesConfigLoader struct {
	Getenv               func(string) string
	UserHomeDir          func() (string, error)
	InClusterConfig      func() (*rest.Config, error)
	BuildConfigFromFlags func(masterURL string, kubeconfigPath string) (*rest.Config, error)
}

func KubernetesJobCreatorFromEnv(config JobConfig, getenv func(string) string) (KubernetesJobCreator, string, error) {
	restConfig, source, err := KubernetesRESTConfigFromEnv(getenv)
	if err != nil {
		return KubernetesJobCreator{}, source, err
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return KubernetesJobCreator{}, source, fmt.Errorf("create Kubernetes clientset: %w", err)
	}

	return NewKubernetesJobCreator(config, clientset.BatchV1()), source, nil
}

func KubernetesRESTConfigFromEnv(getenv func(string) string) (*rest.Config, string, error) {
	return KubernetesConfigLoader{Getenv: getenv}.RESTConfig()
}

func (l KubernetesConfigLoader) RESTConfig() (*rest.Config, string, error) {
	l.withDefaults()

	if config, err := l.InClusterConfig(); err == nil {
		return config, "in-cluster", nil
	}

	kubeconfigPath := strings.TrimSpace(l.Getenv("KUBECONFIG"))
	if kubeconfigPath == "" {
		home, err := l.UserHomeDir()
		if err == nil && strings.TrimSpace(home) != "" {
			kubeconfigPath = filepath.Join(home, ".kube", "config")
		}
	}
	if kubeconfigPath == "" {
		return nil, "", errors.New("no in-cluster config or local kubeconfig path available")
	}

	config, err := l.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, kubeconfigPath, fmt.Errorf("load local kubeconfig %s: %w", kubeconfigPath, err)
	}
	return config, kubeconfigPath, nil
}

func NewKubernetesJobCreator(config JobConfig, batchClient batchtypedv1.BatchV1Interface) KubernetesJobCreator {
	namespace := firstNonEmpty(config.Namespace, defaultNamespace)
	return KubernetesJobCreator{
		Jobs: batchClient.Jobs(namespace),
	}
}

func (l *KubernetesConfigLoader) withDefaults() {
	if l.Getenv == nil {
		l.Getenv = os.Getenv
	}
	if l.UserHomeDir == nil {
		l.UserHomeDir = os.UserHomeDir
	}
	if l.InClusterConfig == nil {
		l.InClusterConfig = rest.InClusterConfig
	}
	if l.BuildConfigFromFlags == nil {
		l.BuildConfigFromFlags = clientcmd.BuildConfigFromFlags
	}
}
