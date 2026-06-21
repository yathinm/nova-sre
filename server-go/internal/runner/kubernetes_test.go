package runner

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestKubernetesConfigLoaderPrefersInClusterConfig(t *testing.T) {
	buildCalled := false
	config, source, err := KubernetesConfigLoader{
		Getenv:          emptyEnv,
		InClusterConfig: func() (*rest.Config, error) { return &rest.Config{Host: "https://cluster.local"}, nil },
		BuildConfigFromFlags: func(string, string) (*rest.Config, error) {
			buildCalled = true
			return nil, errors.New("should not build local config")
		},
	}.RESTConfig()
	if err != nil {
		t.Fatalf("RESTConfig returned error: %v", err)
	}
	if config.Host != "https://cluster.local" {
		t.Fatalf("expected in-cluster host, got %q", config.Host)
	}
	if source != "in-cluster" {
		t.Fatalf("expected in-cluster source, got %q", source)
	}
	if buildCalled {
		t.Fatal("expected in-cluster config to skip local kubeconfig")
	}
}

func TestKubernetesConfigLoaderFallsBackToKubeconfigEnv(t *testing.T) {
	var gotPath string
	config, source, err := KubernetesConfigLoader{
		Getenv: func(key string) string {
			if key == "KUBECONFIG" {
				return "/tmp/minikube-config"
			}
			return ""
		},
		InClusterConfig: func() (*rest.Config, error) { return nil, errors.New("not in cluster") },
		BuildConfigFromFlags: func(_ string, kubeconfigPath string) (*rest.Config, error) {
			gotPath = kubeconfigPath
			return &rest.Config{Host: "https://minikube.local"}, nil
		},
	}.RESTConfig()
	if err != nil {
		t.Fatalf("RESTConfig returned error: %v", err)
	}
	if gotPath != "/tmp/minikube-config" {
		t.Fatalf("expected KUBECONFIG path, got %q", gotPath)
	}
	if source != "/tmp/minikube-config" {
		t.Fatalf("expected source to match KUBECONFIG path, got %q", source)
	}
	if config.Host != "https://minikube.local" {
		t.Fatalf("expected local host, got %q", config.Host)
	}
}

func TestKubernetesConfigLoaderFallsBackToDefaultKubeconfig(t *testing.T) {
	var gotPath string
	_, source, err := KubernetesConfigLoader{
		Getenv:          emptyEnv,
		UserHomeDir:     func() (string, error) { return "/home/nova", nil },
		InClusterConfig: func() (*rest.Config, error) { return nil, errors.New("not in cluster") },
		BuildConfigFromFlags: func(_ string, kubeconfigPath string) (*rest.Config, error) {
			gotPath = kubeconfigPath
			return &rest.Config{Host: "https://default-kubeconfig.local"}, nil
		},
	}.RESTConfig()
	if err != nil {
		t.Fatalf("RESTConfig returned error: %v", err)
	}
	if gotPath != "/home/nova/.kube/config" {
		t.Fatalf("expected default kubeconfig path, got %q", gotPath)
	}
	if source != "/home/nova/.kube/config" {
		t.Fatalf("expected source to match default kubeconfig path, got %q", source)
	}
}

func TestKubernetesJobCreatorCreatesJobWithFakeClient(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	creator := NewKubernetesJobCreator(JobConfig{Namespace: "runner-jobs"}, clientset.BatchV1())
	job, err := BuildGitHubEventJob(JobConfig{
		Namespace: "runner-jobs",
		Image:     "ghcr.io/example/nova-runner:test",
	}, Event{
		DeliveryID: "delivery-123",
		Type:       "push",
		Body:       []byte(`{"repository":{"full_name":"acme/widgets"},"after":"abcdef"}`),
	})
	if err != nil {
		t.Fatalf("BuildGitHubEventJob returned error: %v", err)
	}

	created, err := creator.Create(context.Background(), job)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if created.GenerateName != "nova-sre-push-" {
		t.Fatalf("expected generated job prefix, got %q", created.GenerateName)
	}

	jobs, err := clientset.BatchV1().Jobs("runner-jobs").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(jobs.Items) != 1 {
		t.Fatalf("expected one fake Kubernetes Job, got %d", len(jobs.Items))
	}
	assertContainerEnv(t, &jobs.Items[0], "GITHUB_REPOSITORY", "acme/widgets")
	assertContainerEnv(t, &jobs.Items[0], "GITHUB_SHA", "abcdef")
}

func emptyEnv(string) string {
	return ""
}
