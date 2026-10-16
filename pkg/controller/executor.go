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
	"fmt"

	"github.com/gke-labs/eval-runner/pkg/discovery"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// PodExecutor executes a command inside a specific pod container and captures stdout and stderr.
type PodExecutor interface {
	Exec(ctx context.Context, namespace, podName, containerName string, cmd []string) (stdout string, stderr string, err error)
}

// SPDYPodExecutor implements PodExecutor using client-go's remotecommand SPDY executor.
type SPDYPodExecutor struct {
	Config      *restclient.Config
	Clientset   kubernetes.Interface
	StdoutLimit int64
	StderrLimit int64
}

func (e *SPDYPodExecutor) Exec(ctx context.Context, namespace, podName, containerName string, cmd []string) (string, string, error) {
	req := e.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   cmd,
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(e.Config, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("failed to create SPDY executor: %w", err)
	}

	stdoutLimit := e.StdoutLimit
	if stdoutLimit <= 0 {
		stdoutLimit = discovery.MaxStdoutBytes
	}
	stderrLimit := e.StderrLimit
	if stderrLimit <= 0 {
		stderrLimit = discovery.MaxStderrBytes
	}

	stdoutWriter := discovery.NewLimitWriter(stdoutLimit, "stdout")
	stderrWriter := discovery.NewLimitWriter(stderrLimit, "stderr")

	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: stdoutWriter,
		Stderr: stderrWriter,
	})

	if stdoutWriter.LimitExceeded() {
		return stdoutWriter.String(), stderrWriter.String(), &discovery.LimitError{
			Stream: "stdout",
			Limit:  stdoutLimit,
		}
	}
	if stderrWriter.LimitExceeded() {
		return stdoutWriter.String(), stderrWriter.String(), &discovery.LimitError{
			Stream: "stderr",
			Limit:  stderrLimit,
		}
	}

	return stdoutWriter.String(), stderrWriter.String(), err
}
