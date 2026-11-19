// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/eval-runner/api/v1alpha1"
	"github.com/gke-labs/eval-runner/pkg/discovery"
	"github.com/gke-labs/eval-runner/pkg/sandboxclient"
	portalsv1 "github.com/gke-labs/service-portals/pkg/portals/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
)

type fakeServicePortalClient struct {
	bindFunc func(ctx context.Context, podUID string, policy *portalsv1.EgressPolicy, secrets map[string][]byte) error
	calls    int
	lastUID  string
	lastPol  *portalsv1.EgressPolicy
	lastSecs map[string][]byte
}

func (f *fakeServicePortalClient) Bind(ctx context.Context, podUID string, policy *portalsv1.EgressPolicy, secrets map[string][]byte) error {
	f.calls++
	f.lastUID = podUID
	f.lastPol = policy
	f.lastSecs = secrets
	if f.bindFunc != nil {
		return f.bindFunc(ctx, podUID, policy, secrets)
	}
	return nil
}

type recordingSandboxClient struct {
	commandsExecuted []string
}

func (m *recordingSandboxClient) Execute(ctx context.Context, cfg sandboxclient.ProcessConfig) (*sandboxclient.ExecuteResult, error) {
	cmdStr := strings.Join(cfg.Command, " ")
	m.commandsExecuted = append(m.commandsExecuted, cmdStr)
	return &sandboxclient.ExecuteResult{ExitCode: 0}, nil
}

func (m *recordingSandboxClient) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (m *recordingSandboxClient) Close() error {
	return nil
}

func TestBuildServicePortalEgressPolicy(t *testing.T) {
	t.Run("empty hosts produces empty policy without rules", func(t *testing.T) {
		p := BuildServicePortalEgressPolicy(nil)
		if len(p.GetRules()) != 0 {
			t.Errorf("expected 0 rules for nil hosts, got %d", len(p.GetRules()))
		}
		if err := p.Validate(); err != nil {
			t.Errorf("empty policy failed validation: %v", err)
		}
	})

	t.Run("deduplicates, trims whitespace, and constructs HTTP and HTTPS rules", func(t *testing.T) {
		hosts := []string{"  github.com ", "deb.debian.org", "github.com", "", "  "}
		p := BuildServicePortalEgressPolicy(hosts)
		if err := p.Validate(); err != nil {
			t.Fatalf("policy failed validation: %v", err)
		}
		rules := p.GetRules()
		if len(rules) != 2 {
			t.Fatalf("expected 2 rules (HTTP + HTTPS), got %d", len(rules))
		}
		httpRule := rules[0].GetHttp()
		httpsRule := rules[1].GetHttps()
		if httpRule == nil || httpsRule == nil {
			t.Fatalf("expected HTTP and HTTPS rules, got http=%v, https=%v", httpRule, httpsRule)
		}
		expectedHosts := []string{"github.com", "deb.debian.org"}
		if !reflect.DeepEqual(httpRule.GetHostnames(), expectedHosts) {
			t.Errorf("expected HTTP hostnames %v, got %v", expectedHosts, httpRule.GetHostnames())
		}
		if !reflect.DeepEqual(httpsRule.GetHostnames(), expectedHosts) {
			t.Errorf("expected HTTPS hostnames %v, got %v", expectedHosts, httpsRule.GetHostnames())
		}
	})

	t.Run("default sandbox egress hosts produce valid policy", func(t *testing.T) {
		p := BuildServicePortalEgressPolicy(discovery.DefaultSandboxEgressHosts)
		if err := p.Validate(); err != nil {
			t.Fatalf("default egress hosts policy failed validation: %v", err)
		}
		if len(p.GetRules()) != 2 {
			t.Fatalf("expected 2 rules, got %d", len(p.GetRules()))
		}
	})
}

func TestRunReconciler_ServicePortalPolicy_Hosts(t *testing.T) {
	t.Run("falls back to DefaultSandboxEgressHosts when empty", func(t *testing.T) {
		r := &RunReconciler{}
		pol := r.servicePortalPolicy()
		if len(pol.GetRules()) != 2 {
			t.Fatalf("expected 2 rules, got %d", len(pol.GetRules()))
		}
		if !reflect.DeepEqual(pol.GetRules()[0].GetHttp().GetHostnames(), discovery.DefaultSandboxEgressHosts) {
			t.Errorf("expected hostnames to match DefaultSandboxEgressHosts")
		}
	})

	t.Run("uses SandboxEgressHosts when set", func(t *testing.T) {
		r := &RunReconciler{
			SandboxEgressHosts: []string{"repo.example.com", "api.example.com"},
		}
		pol := r.servicePortalPolicy()
		expected := []string{"repo.example.com", "api.example.com"}
		if !reflect.DeepEqual(pol.GetRules()[0].GetHttp().GetHostnames(), expected) {
			t.Errorf("expected hostnames %v, got %v", expected, pol.GetRules()[0].GetHttp().GetHostnames())
		}
	})
}

func TestRunReconciler_ServicePortalConfigured(t *testing.T) {
	r := &RunReconciler{}
	if r.servicePortalConfigured() {
		t.Errorf("expected servicePortalConfigured to be false when client is nil")
	}
	r.ServicePortalClient = &fakeServicePortalClient{}
	if !r.servicePortalConfigured() {
		t.Errorf("expected servicePortalConfigured to be true when client is set")
	}
}

func TestRunReconciler_BindServicePortalPolicy(t *testing.T) {
	t.Run("returns error when pod UID is empty", func(t *testing.T) {
		client := &fakeServicePortalClient{}
		r := &RunReconciler{
			ServicePortalClient: client,
		}
		err := r.bindServicePortalPolicy(context.Background(), &corev1.Pod{})
		if err == nil || !strings.Contains(err.Error(), "pod UID is empty") {
			t.Errorf("expected pod UID is empty error, got: %v", err)
		}
		if client.calls != 0 {
			t.Errorf("expected 0 client calls, got %d", client.calls)
		}
	})

	t.Run("binds policy with pod UID", func(t *testing.T) {
		client := &fakeServicePortalClient{}
		r := &RunReconciler{
			ServicePortalClient: client,
			SandboxEgressHosts:  []string{"custom.example.com"},
		}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				UID: types.UID("pod-uid-1234"),
			},
		}
		err := r.bindServicePortalPolicy(context.Background(), pod)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if client.calls != 1 {
			t.Fatalf("expected 1 client call, got %d", client.calls)
		}
		if client.lastUID != "pod-uid-1234" {
			t.Errorf("expected pod UID pod-uid-1234, got %q", client.lastUID)
		}
		if client.lastPol == nil || len(client.lastPol.GetRules()) != 2 {
			t.Fatalf("expected bound policy with 2 rules, got %v", client.lastPol)
		}
		if hostnames := client.lastPol.GetRules()[0].GetHttp().GetHostnames(); !reflect.DeepEqual(hostnames, []string{"custom.example.com"}) {
			t.Errorf("expected hostnames [custom.example.com], got %v", hostnames)
		}
	})

	t.Run("propagates refusal error", func(t *testing.T) {
		client := &fakeServicePortalClient{
			bindFunc: func(ctx context.Context, podUID string, policy *portalsv1.EgressPolicy, secrets map[string][]byte) error {
				return errors.New("bind refused: pod not registered on node agent")
			},
		}
		r := &RunReconciler{
			ServicePortalClient: client,
		}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				UID: types.UID("pod-uid-refused"),
			},
		}
		err := r.bindServicePortalPolicy(context.Background(), pod)
		if err == nil || !strings.Contains(err.Error(), "pod not registered on node agent") {
			t.Errorf("expected refused error, got: %v", err)
		}
	})
}

func TestRunReconciler_ExecuteTask_ServicePortal(t *testing.T) {
	run := &v1alpha1.Run{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run",
			Namespace: "eval-runner",
		},
		Status: v1alpha1.RunStatus{
			ExecutionDeadline: &metav1.Time{Time: time.Now().Add(10 * time.Minute)},
			Tasks: []v1alpha1.TaskStatus{
				{
					ID:    "task-sb",
					Phase: v1alpha1.TaskPhasePending,
				},
			},
		},
	}
	sb := &sandboxv1beta1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sb",
			Namespace: "eval-runner-sandbox",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sb",
			Namespace: "eval-runner-sandbox",
			UID:       types.UID("pod-uid-test"),
		},
		Status: corev1.PodStatus{
			PodIP: "10.0.0.1",
		},
	}

	manifest := &v1alpha1.Manifest{
		Tasks: []v1alpha1.ManifestTask{
			{
				ID:      "task-sb",
				Image:   "test-image",
				Command: []string{"echo", "hello"},
			},
		},
	}

	t.Run("when bind is refused, task fails and commands never run", func(t *testing.T) {
		portalClient := &fakeServicePortalClient{
			bindFunc: func(ctx context.Context, podUID string, policy *portalsv1.EgressPolicy, secrets map[string][]byte) error {
				return errors.New("bind refused: node agent rejected policy")
			},
		}
		sbClient := &recordingSandboxClient{}
		r := &RunReconciler{
			ServicePortalClient:  portalClient,
			SandboxClient:       sbClient,
			SandboxNamespace:    "eval-runner-sandbox",
			ExecutionDeadline:   10 * time.Minute,
			ExecutionStepTimeout: 2 * time.Minute,
		}
		r.setAuthToken("eval-runner-sandbox/test-sb", "test-token")
		r.setCachedManifest("eval-runner/test-run", manifest)

		res := r.executeTask(context.Background(), run, "task-sb", sb, pod)
		if res.phase != v1alpha1.TaskPhaseFailed {
			t.Errorf("expected task phase Failed, got %v", res.phase)
		}
		if !strings.Contains(res.message, "node agent rejected policy") {
			t.Errorf("expected message to mention rejection, got %q", res.message)
		}
		if len(sbClient.commandsExecuted) != 0 {
			t.Fatalf("expected 0 commands executed unbound, but ran: %v", sbClient.commandsExecuted)
		}
	})

	t.Run("when bind succeeds, task executes commands", func(t *testing.T) {
		portalClient := &fakeServicePortalClient{}
		sbClient := &recordingSandboxClient{}
		r := &RunReconciler{
			ServicePortalClient:  portalClient,
			SandboxClient:       sbClient,
			SandboxNamespace:    "eval-runner-sandbox",
			ExecutionDeadline:   10 * time.Minute,
			ExecutionStepTimeout: 2 * time.Minute,
		}
		r.setAuthToken("eval-runner-sandbox/test-sb", "test-token")
		r.setCachedManifest("eval-runner/test-run", manifest)

		res := r.executeTask(context.Background(), run, "task-sb", sb, pod)
		if portalClient.calls != 1 {
			t.Fatalf("expected 1 portal bind call, got %d", portalClient.calls)
		}
		if portalClient.lastUID != "pod-uid-test" {
			t.Errorf("expected pod UID pod-uid-test, got %q", portalClient.lastUID)
		}
		if len(sbClient.commandsExecuted) == 0 {
			t.Errorf("expected commands to execute after accepted bind, got 0")
		}
		if res.phase == v1alpha1.TaskPhaseFailed && strings.Contains(res.message, "service portal") {
			t.Errorf("unexpected service portal failure: %s", res.message)
		}
	})

	t.Run("when service portal is not configured, runs without binding", func(t *testing.T) {
		sbClient := &recordingSandboxClient{}
		r := &RunReconciler{
			ServicePortalClient:  nil,
			SandboxClient:       sbClient,
			SandboxNamespace:    "eval-runner-sandbox",
			ExecutionDeadline:   10 * time.Minute,
			ExecutionStepTimeout: 2 * time.Minute,
		}
		r.setAuthToken("eval-runner-sandbox/test-sb", "test-token")
		r.setCachedManifest("eval-runner/test-run", manifest)

		_ = r.executeTask(context.Background(), run, "task-sb", sb, pod)
		if len(sbClient.commandsExecuted) == 0 {
			t.Errorf("expected commands to execute without service portal configured")
		}
	})
}
