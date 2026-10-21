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

// Package sinkconfig provides helpers for constructing ArtifactSink instances
// and reading artifact storage configuration from the cluster ConfigMap.
//
// It is intentionally decoupled from pkg/controller so that lightweight consumers
// like eval-runner-ui can initialize storage sinks without importing controller
// dependencies (sandbox clients, GitHub integration, PKI, discovery).
package sinkconfig

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/artifacts/gcs"
	"github.com/gke-labs/eval-runner/pkg/configkeys"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// DefaultConfigMapName is the default name for the eval-runner configuration ConfigMap.
	DefaultConfigMapName = configkeys.DefaultConfigMapName

	// DefaultNamespace is the default namespace where eval-runner components and configuration reside.
	DefaultNamespace = "eval-runner"
)

// SinkFactory is a constructor function for ArtifactSink instances.
type SinkFactory func(ctx context.Context, bucket, dir string, maxUploadBytes int64) (artifacts.ArtifactSink, error)

// Config holds the artifact storage settings extracted from the cluster ConfigMap.
type Config struct {
	ArtifactBucket string
	ArtifactDir    string
}

// NewArtifactSink constructs an ArtifactSink using the settings in c.
func (c Config) NewArtifactSink(ctx context.Context, maxUploadBytes int64) (artifacts.ArtifactSink, error) {
	return NewArtifactSink(ctx, c.ArtifactBucket, c.ArtifactDir, maxUploadBytes)
}

// LoadConfigMap retrieves the cluster ConfigMap using reader.
// Returns (nil, nil) if reader is nil or if the ConfigMap does not exist.
func LoadConfigMap(ctx context.Context, reader client.Reader, namespace, name string) (*corev1.ConfigMap, error) {
	if reader == nil {
		return nil, nil
	}
	if name == "" {
		name = DefaultConfigMapName
	}
	if namespace == "" {
		namespace = DefaultNamespace
	}

	var cm corev1.ConfigMap
	key := types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}
	if err := reader.Get(ctx, key, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get configmap %s/%s: %w", namespace, name, err)
	}
	return &cm, nil
}

// LoadArtifactConfig reads the cluster ConfigMap and extracts artifactBucket and artifactDir.
// If name is empty, DefaultConfigMapName is used.
// If namespace is empty, DefaultNamespace is used.
// If the ConfigMap is not found, an empty Config is returned with a nil error.
func LoadArtifactConfig(ctx context.Context, reader client.Reader, namespace, name string) (Config, error) {
	cm, err := LoadConfigMap(ctx, reader, namespace, name)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if cm != nil && cm.Data != nil {
		if b := strings.TrimSpace(cm.Data[configkeys.ConfigMapKeyArtifactBucket]); b != "" && !strings.EqualFold(b, configkeys.ConfigSentinelDisabled) {
			cfg.ArtifactBucket = b
		}
		if d := strings.TrimSpace(cm.Data[configkeys.ConfigMapKeyArtifactDir]); d != "" && !strings.EqualFold(d, configkeys.ConfigSentinelDisabled) {
			cfg.ArtifactDir = d
		}
	}
	return cfg, nil
}

// NewArtifactSink constructs an ArtifactSink based on the provided bucket and local directory.
// If bucket is non-empty, a GCSSink is returned (with optional maxUploadBytes cap).
// If dir is non-empty, a LocalDirSink is returned.
// If neither is configured, nil is returned without error.
func NewArtifactSink(ctx context.Context, bucket, dir string, maxUploadBytes int64) (artifacts.ArtifactSink, error) {
	bucket = strings.TrimSpace(bucket)
	if strings.EqualFold(bucket, configkeys.ConfigSentinelDisabled) {
		bucket = ""
	}
	dir = strings.TrimSpace(dir)
	if strings.EqualFold(dir, configkeys.ConfigSentinelDisabled) {
		dir = ""
	}
	if bucket != "" {
		var gcsOpts []gcs.Option
		if maxUploadBytes > 0 {
			gcsOpts = append(gcsOpts, gcs.WithMaxUploadBytes(maxUploadBytes))
		}
		return gcs.New(ctx, bucket, gcsOpts...)
	}
	if dir != "" {
		return &artifacts.LocalDirSink{BaseDir: dir}, nil
	}
	return nil, nil
}

// WatchConfigMap watches a single ConfigMap by name in namespace using the Kubernetes API.
// It uses only get and watch operations scoped to the specified ConfigMap name.
// When the ConfigMap is added or modified, onChange is called with the ConfigMap.
// When the ConfigMap is deleted, onChange is called with nil.
func WatchConfigMap(ctx context.Context, client kubernetes.Interface, namespace, name string, onChange func(cm *corev1.ConfigMap)) error {
	if client == nil {
		return errors.New("kubernetes client is nil")
	}
	if name == "" {
		name = DefaultConfigMapName
	}

	fieldSelector := "metadata.name=" + name

	return WatchNamedResource(ctx, WatchNamedResourceOptions{
		Get: func(ctx context.Context) (metav1.Object, error) {
			return client.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
		},
		Watch: func(ctx context.Context, rv string) (watch.Interface, error) {
			return client.CoreV1().ConfigMaps(namespace).Watch(ctx, metav1.ListOptions{
				FieldSelector:       fieldSelector,
				ResourceVersion:     rv,
				AllowWatchBookmarks: true,
			})
		},
		OnEvent: func(eventType watch.EventType, obj runtime.Object) {
			if onChange == nil {
				return
			}
			if eventType == watch.Deleted || obj == nil {
				onChange(nil)
				return
			}
			if cm, ok := obj.(*corev1.ConfigMap); ok {
				onChange(cm)
			}
		},
	})
}
