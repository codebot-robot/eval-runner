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
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// DeriveSampleRunnerImage derives the sample-runner image reference from the eval-runner controller image reference.
// It replaces the final path segment "eval-runner" with "sample-runner", keeping the prefix and tag.
// If the image uses a digest reference (@sha256:), or does not end in eval-runner:<tag>, an error is returned.
func DeriveSampleRunnerImage(controllerImage string) (string, error) {
	if strings.Contains(controllerImage, "@") {
		return "", fmt.Errorf("controller image %q uses a digest reference and cannot be mapped to sample-runner; specify --sample-runner-image explicitly", controllerImage)
	}

	lastSlash := strings.LastIndex(controllerImage, "/")
	var prefix, remainder string
	if lastSlash != -1 {
		prefix = controllerImage[:lastSlash+1]
		remainder = controllerImage[lastSlash+1:]
	} else {
		remainder = controllerImage
	}

	parts := strings.Split(remainder, ":")
	if len(parts) != 2 || parts[0] != "eval-runner" || parts[1] == "" {
		return "", fmt.Errorf("controller image %q does not end in eval-runner:<tag>; specify --sample-runner-image explicitly", controllerImage)
	}

	return prefix + "sample-runner:" + parts[1], nil
}

// isEvalRunnerImage returns true if the image reference repository name is "eval-runner".
func isEvalRunnerImage(image string) bool {
	if image == "" {
		return false
	}
	lastSlash := strings.LastIndex(image, "/")
	remainder := image
	if lastSlash != -1 {
		remainder = image[lastSlash+1:]
	}
	parts := strings.Split(remainder, ":")
	if len(parts) >= 1 && parts[0] == "eval-runner" {
		return true
	}
	atParts := strings.Split(remainder, "@")
	return len(atParts) >= 1 && atParts[0] == "eval-runner"
}

// FindEvalRunnerImage inspects the pod spec and returns the image reference of the eval-runner container.
func FindEvalRunnerImage(pod *corev1.Pod) (string, error) {
	if pod == nil || len(pod.Spec.Containers) == 0 {
		return "", fmt.Errorf("pod has no containers")
	}

	// 1. Look for container named "eval-runner"
	for _, c := range pod.Spec.Containers {
		if c.Name == "eval-runner" && c.Image != "" {
			return c.Image, nil
		}
	}

	// 2. Look for container named "controller"
	for _, c := range pod.Spec.Containers {
		if c.Name == "controller" && c.Image != "" {
			return c.Image, nil
		}
	}

	// 3. Look for any container whose image base name is eval-runner
	for _, c := range pod.Spec.Containers {
		if isEvalRunnerImage(c.Image) {
			return c.Image, nil
		}
	}

	// 4. If there is exactly one container, return its image
	if len(pod.Spec.Containers) == 1 && pod.Spec.Containers[0].Image != "" {
		return pod.Spec.Containers[0].Image, nil
	}

	return "", fmt.Errorf("could not find eval-runner container in pod %s/%s", pod.Namespace, pod.Name)
}
