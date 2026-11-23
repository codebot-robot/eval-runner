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
	"time"

	"github.com/gke-labs/eval-runner/pkg/artifacts"
	"github.com/gke-labs/eval-runner/pkg/artifacts/gcs"
	"github.com/gke-labs/eval-runner/pkg/configkeys"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
type SinkFactory func(ctx context.Context, config Config) (artifacts.ArtifactSink, error)

// Config holds the artifact storage settings extracted from the cluster ConfigMap.
type Config struct {
	ArtifactBucket              string
	ArtifactDir                 string
	ArtifactCacheDirectory      string
	GCPWorkloadIdentityProvider string
	ArtifactMaxUploadBytes      int64
}

// NewArtifactSink constructs an ArtifactSink using the settings in c.
func (c Config) NewArtifactSink(ctx context.Context) (artifacts.ArtifactSink, error) {
	return NewArtifactSink(ctx, c)
}

// ResolveConfig resolves artifact storage configuration from a ConfigMap and flag values.
// Precedence: explicit flag > ConfigMap > built-in default.
func ResolveConfig(cm *corev1.ConfigMap, flags Config) Config {
	cfg := Config{
		ArtifactCacheDirectory: strings.TrimSpace(flags.ArtifactCacheDirectory),
		ArtifactMaxUploadBytes: flags.ArtifactMaxUploadBytes,
	}
	if cm != nil && cm.Data != nil {
		if bucket := strings.TrimSpace(cm.Data[configkeys.ConfigMapKeyArtifactBucket]); bucket != "" && !strings.EqualFold(bucket, configkeys.ConfigSentinelDisabled) {
			cfg.ArtifactBucket = bucket
		}
		if directory := strings.TrimSpace(cm.Data[configkeys.ConfigMapKeyArtifactDir]); directory != "" && !strings.EqualFold(directory, configkeys.ConfigSentinelDisabled) {
			cfg.ArtifactDir = directory
		}
		if provider := strings.TrimSpace(cm.Data[configkeys.ConfigMapKeyGCPWorkloadIdentityProvider]); provider != "" {
			cfg.GCPWorkloadIdentityProvider = provider
		}
	}
	if bucket := strings.TrimSpace(flags.ArtifactBucket); bucket != "" {
		if strings.EqualFold(bucket, configkeys.ConfigSentinelDisabled) {
			cfg.ArtifactBucket = ""
		} else {
			cfg.ArtifactBucket = bucket
		}
	}
	if dir := strings.TrimSpace(flags.ArtifactDir); dir != "" {
		if strings.EqualFold(dir, configkeys.ConfigSentinelDisabled) {
			cfg.ArtifactDir = ""
		} else {
			cfg.ArtifactDir = dir
		}
	}
	return cfg
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

// NewArtifactSink constructs an ArtifactSink based on the provided configuration.
// If bucket is non-empty, a GCSSink is returned (with optional maxUploadBytes cap, cache directory, and workload identity provider).
// If dir is non-empty, a LocalDirSink is returned.
// If neither is configured, nil is returned without error.
func NewArtifactSink(ctx context.Context, config Config) (artifacts.ArtifactSink, error) {
	bucket := strings.TrimSpace(config.ArtifactBucket)
	if strings.EqualFold(bucket, configkeys.ConfigSentinelDisabled) {
		bucket = ""
	}
	dir := strings.TrimSpace(config.ArtifactDir)
	if strings.EqualFold(dir, configkeys.ConfigSentinelDisabled) {
		dir = ""
	}
	if bucket != "" {
		var gcsOptions []gcs.Option
		if config.ArtifactMaxUploadBytes > 0 {
			gcsOptions = append(gcsOptions, gcs.WithMaxUploadBytes(config.ArtifactMaxUploadBytes))
		}
		if provider := strings.TrimSpace(config.GCPWorkloadIdentityProvider); provider != "" {
			gcsOptions = append(gcsOptions, gcs.WithWorkloadIdentityProvider(provider))
		}
		if cacheDirectory := strings.TrimSpace(config.ArtifactCacheDirectory); cacheDirectory != "" {
			gcsOptions = append(gcsOptions, gcs.WithCacheDirectory(cacheDirectory))
		}
		return gcs.New(ctx, bucket, gcsOptions...)
	}
	if dir != "" {
		return &artifacts.LocalDirSink{BaseDir: dir}, nil
	}
	return nil, nil
}

// ProbeArtifactSink probes credentials and bucket access for sinks that support access checking (such as GCSSink).
// Sinks that do not implement AccessChecker return nil without error.
func ProbeArtifactSink(ctx context.Context, sink artifacts.ArtifactSink) error {
	if rs, ok := sink.(*artifacts.ReloadableSink); ok {
		sink = rs.Current()
	}
	if checker, ok := sink.(artifacts.AccessChecker); ok {
		return checker.CheckAccess(ctx)
	}
	return nil
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

	return WatchNamedObject(
		ctx,
		name,
		func(ctx context.Context) (metav1.Object, error) {
			return client.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
		},
		func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
			return client.CoreV1().ConfigMaps(namespace).Watch(ctx, opts)
		},
		func(obj metav1.Object) {
			if obj == nil {
				onChange(nil)
				return
			}
			if cm, ok := obj.(*corev1.ConfigMap); ok {
				onChange(cm)
			}
		},
	)
}

// WatchNamedObject watches a single named Kubernetes object using provided get and watch functions.
// It handles watch.Bookmark (refreshing resourceVersion) and watch.Error (stopping, short sleep,
// re-Get, and triggering onChange) with AllowWatchBookmarks: true.
// On Added and Modified events, onChange is invoked with the updated object.
// On Deleted events or when the object is not found, onChange is invoked with nil.
func WatchNamedObject(
	ctx context.Context,
	name string,
	getFn func(ctx context.Context) (metav1.Object, error),
	watchFn func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error),
	onChange func(obj metav1.Object),
) error {
	if getFn == nil || watchFn == nil || onChange == nil {
		return errors.New("getFn, watchFn, and onChange must not be nil")
	}

	fieldSelector := "metadata.name=" + name

	// Initial Get
	var rv string
	obj, err := getFn(ctx)
	if err == nil {
		rv = obj.GetResourceVersion()
		onChange(obj)
	} else if apierrors.IsNotFound(err) {
		rv = ""
		onChange(nil)
	} else if ctx.Err() != nil {
		return ctx.Err()
	} else {
		return fmt.Errorf("failed to get initial object %s: %w", name, err)
	}

	backoff := 1 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		watcher, err := watchFn(ctx, metav1.ListOptions{
			FieldSelector:       fieldSelector,
			ResourceVersion:     rv,
			AllowWatchBookmarks: true,
		})
		if err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			obj, err := getFn(ctx)
			if err == nil {
				rv = obj.GetResourceVersion()
				onChange(obj)
			} else if apierrors.IsNotFound(err) {
				rv = ""
				onChange(nil)
			}
			continue
		}

		backoff = 1 * time.Second

		stopped := false
		for !stopped {
			select {
			case <-ctx.Done():
				watcher.Stop()
				return ctx.Err()
			case event, ok := <-watcher.ResultChan():
				if !ok {
					stopped = true
					break
				}
				switch event.Type {
				case watch.Added, watch.Modified:
					if updatedObj, ok := event.Object.(metav1.Object); ok {
						if updatedObj.GetResourceVersion() != "" && updatedObj.GetResourceVersion() == rv {
							continue
						}
						rv = updatedObj.GetResourceVersion()
						onChange(updatedObj)
					}
				case watch.Deleted:
					rv = ""
					onChange(nil)
				case watch.Bookmark:
					if meta, ok := event.Object.(metav1.Object); ok {
						rv = meta.GetResourceVersion()
					}
				case watch.Error:
					stopped = true
					break
				}
			}
		}
		watcher.Stop()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}

		obj, err := getFn(ctx)
		if err == nil {
			rv = obj.GetResourceVersion()
			onChange(obj)
		} else if apierrors.IsNotFound(err) {
			rv = ""
			onChange(nil)
		}
	}
}
